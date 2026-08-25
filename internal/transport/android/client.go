package android

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brentyates/squaregolf-connector/internal/core"
)

// Client implements core.BluetoothClient (internal/core/bluetooth_client.go:9) by
// delegating to a Kotlin-side Bridge, and additionally implements the two
// off-interface setters that internal/core/bluetooth_manager.go:60 asserts
// against. With the sanctioned widening of that assertion to a capability
// interface, an *android.Client receives phase and connection-lost callbacks
// exactly as the TinyGo client does.
//
// NOTE ON LOGGING: Go's stdlib log reaches Logcat as tag GoLog at INFO priority,
// so there is no debug level in this package. Never log ev.data, a decoded
// metric, or any packet bytes from here -- CLAUDE.md forbids shot spam at info
// level because it buries the connection diagnostics you actually need.
type Client struct {
	// bridge is written once, at construction, and never mutated. No lock.
	bridge Bridge

	// connected is the cached link state. core.LaunchMonitor calls IsConnected()
	// on every command, every heartbeat and every notification
	// (internal/core/launch_monitor.go:672, 745, 791 and elsewhere). Asking Kotlin
	// each time would be an FFI hop on the hottest path in the app and could
	// deadlock against a Kotlin lock held by a thread already blocked in a Bridge
	// call, so the state is mirrored here and refreshed by OnConnectionStateChanged.
	connected atomic.Bool

	// gen counts link generations. It is incremented on every observed
	// disconnect, and Connect uses it to refuse to promote the cached state if
	// the link it was establishing has already died. Without it a disconnect
	// arriving between Bridge.Connect returning and Connect's own bookkeeping
	// would be overwritten by an unconditional Store(true), leaving IsConnected()
	// permanently true on a dead link -- and Android delivers
	// onConnectionStateChange(DISCONNECTED) only once, so nothing would ever
	// correct it.
	gen atomic.Uint64

	closed atomic.Bool

	// cancelling is set for the duration of a Disconnect so that a call blocked
	// waiting on gattMu gives up instead of queueing behind an operation that is
	// itself stuck inside Kotlin.
	cancelling atomic.Bool

	// gattMu admits one Bridge GATT operation at a time, matching Android's
	// one-outstanding-operation rule. It is held ONLY across the Bridge call and
	// never while any other mutex is held.
	//
	// Disconnect, StopNotifications and StopScan deliberately do not take it.
	// They are the cancel path, and upstream's teardown
	// (internal/core/bluetooth_manager.go:418-430) calls StopNotifications twice
	// BEFORE Disconnect -- so if StopNotifications queued on gattMu behind a
	// Connect blocked inside Kotlin, the cancel path would never reach the
	// Disconnect that was exempted and teardown would wedge forever.
	gattMu sync.Mutex

	// regMu guards handlers. It is never held while a handler runs: a handler can
	// block for ten seconds inside LaunchMonitor.SendCommand
	// (internal/core/launch_monitor.go:649-670).
	regMu    sync.RWMutex
	handlers map[string]func([]byte)

	// devMu guards the discovered-device table and the connected identity.
	devMu           sync.Mutex
	devices         map[string]*discoveredDevice
	devOrder        []string
	scanPrefix      string
	connectedAddr   string
	lastConnectName string

	// cbMu guards the callbacks installed by BluetoothManager.
	cbMu          sync.RWMutex
	onPhaseChange func(core.ConnectionPhase)
	onConnLost    func()

	// Two independent delivery lanes. Notifications must not be able to starve a
	// disconnect: if a notification handler wedges, the lifecycle lane still runs
	// and the application still learns the link is gone.
	notifyCh    chan event
	lifecycleCh chan event

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	dropped atomic.Int64
}

// Compile-time proof that Client satisfies the upstream interface.
var _ core.BluetoothClient = (*Client)(nil)

// CallbackCapableClient is the capability interface that the widened assertion at
// internal/core/bluetooth_manager.go:60 uses in place of the concrete
// *TinyGoBluetoothClient check. It is declared here for documentation and for the
// compile-time guarantee below; package core cannot import this package (import
// cycle), so core declares its own structurally identical unexported interface
// and Go's structural typing joins them.
type CallbackCapableClient interface {
	SetPhaseChangeCallback(func(core.ConnectionPhase))
	SetConnectionLostCallback(func())
}

var _ CallbackCapableClient = (*Client)(nil)

const (
	// notifyQueueDepth is roughly a dozen shots of slack. The consumer only falls
	// behind while a handler is parked in SendCommand's five-second timeouts, by
	// which point the link is already in trouble.
	notifyQueueDepth = 256

	// lifecycleQueueDepth covers phase changes and connection-loss events, which
	// arrive at human speed.
	lifecycleQueueDepth = 32

	// enqueueGrace is how long a binder thread may wait for space in a full queue
	// before the event is dropped. Long enough to ride out a hiccup, far short of
	// the ten seconds that would deadlock GATT delivery.
	enqueueGrace = 250 * time.Millisecond

	// maxDiscovered bounds the device table against an advertisement flood from a
	// mis-implemented Kotlin scan filter.
	maxDiscovered = 64

	// flushTimeout bounds how long Connect waits for the phase events it caused to
	// be delivered before it returns. See flushLifecycle.
	flushTimeout = 2 * time.Second

	// gattWait bounds how long a GATT operation waits for gattMu before giving up.
	// It exceeds the Kotlin-side per-operation timeout budget for a healthy
	// device, so it only fires when Kotlin has already broken its contract.
	gattWait = 15 * time.Second

	// closeWait bounds Close's wait on the delivery goroutines. A handler parked
	// in SendCommand holds a lane for up to ten seconds.
	closeWait = 12 * time.Second
)

var (
	// ErrNoBridge is returned when no Kotlin implementation has been installed. It
	// is a wiring bug, not a runtime condition.
	ErrNoBridge = errors.New("android transport: no bridge installed")

	// ErrClosed is returned after Close.
	ErrClosed = errors.New("android transport: client closed")

	// ErrBusy is returned when a GATT operation could not acquire the single
	// in-flight slot, which means Kotlin is not honouring its timeout contract.
	ErrBusy = errors.New("android transport: another GATT operation is stuck; giving up")

	// errNotConnected mirrors upstream's own wording
	// (internal/core/tinygo_bluetooth_client.go:429).
	errNotConnected = errors.New("android transport: not connected")
)

// discoveredDevice is one advertisement, keyed by address.
type discoveredDevice struct {
	name    string
	address string
	mfgHex  string
}

type eventKind int

const (
	evNotification eventKind = iota
	evPhase
	evConnectionLost
	evFlush
)

type event struct {
	kind  eventKind
	uuid  string
	data  []byte
	phase core.ConnectionPhase
	done  chan struct{}
}

// NewClient returns a Client delegating to bridge and starts its two delivery
// goroutines.
//
// A nil bridge is permitted; every operation then fails with ErrNoBridge rather
// than panicking, which keeps a wiring mistake diagnosable.
//
// The Engine constructs exactly one Client per process and never closes it, so
// the two goroutines live for the process lifetime. Do NOT construct a Client per
// Activity: each one leaks two goroutines and its channels unless Close is called.
func NewClient(bridge Bridge) *Client {
	c := &Client{
		bridge:      bridge,
		handlers:    make(map[string]func([]byte)),
		devices:     make(map[string]*discoveredDevice),
		notifyCh:    make(chan event, notifyQueueDepth),
		lifecycleCh: make(chan event, lifecycleQueueDepth),
		stop:        make(chan struct{}),
	}
	c.wg.Add(2)
	go c.run(c.notifyCh)
	go c.run(c.lifecycleCh)
	return c
}

// Close stops the delivery goroutines and makes every subsequent operation return
// ErrClosed. It is idempotent.
//
// It blocks until both lanes are idle, up to closeWait: a handler parked inside
// LaunchMonitor.SendCommand holds its lane for as long as ten seconds. It must not
// be called from inside a notification handler or a phase callback -- that would
// wait on the goroutine running the caller.
func (c *Client) Close() error {
	c.stopOnce.Do(func() {
		c.closed.Store(true)
		close(c.stop)
	})
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(closeWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		log.Println("android transport: timed out waiting for delivery goroutines to stop")
	}
	return nil
}

// DroppedNotifications reports how many inbound packets were discarded because the
// delivery queue was full or the client was shutting down. Any non-zero value
// means upstream's handler chain could not keep up with the radio and shot data
// was lost. Surface it: it is the only instrument for silent shot loss.
func (c *Client) DroppedNotifications() int64 {
	return c.dropped.Load()
}

// ---------------------------------------------------------------------------
// core.BluetoothClient
// ---------------------------------------------------------------------------

// Connect resolves the target as far as Go can and hands the rest to Kotlin.
//
// It blocks until the device is connected and its services are discovered, or
// until Kotlin throws. Upstream already calls it from a background goroutine
// (internal/core/bluetooth_manager.go:139, 352), so blocking is correct.
func (c *Client) Connect(deviceName, deviceAddress string) error {
	if c.closed.Load() {
		return ErrClosed
	}
	if c.bridge == nil {
		return ErrNoBridge
	}

	// Snapshot the link generation BEFORE crossing to Kotlin. If a disconnect is
	// observed while we are blocked in there, gen moves and we must not promote
	// the cached state on the way out.
	startGen := c.gen.Load()

	c.devMu.Lock()
	c.lastConnectName = deviceName
	prefix := c.scanPrefix
	if deviceAddress == "" {
		// A prior scan may already hold the target, letting Kotlin skip straight
		// to getRemoteDevice with no second scan.
		if d := c.resolveTargetLocked(deviceName, prefix); d != nil {
			deviceAddress = d.address
		}
	}
	c.devMu.Unlock()

	// Not under devMu: a Bridge call must never be made holding a lock that the
	// inbound callbacks (which arrive on other threads while this one is parked)
	// also need.
	if !c.acquireGATT() {
		return ErrBusy
	}
	err := c.bridge.Connect(deviceName, deviceAddress, prefix)
	c.gattMu.Unlock()
	if err != nil {
		return bridgeErr(err, "connect", deviceName)
	}

	// Promote the cached state only if no disconnect landed while we were inside
	// Kotlin. Kotlin's OnConnectionStateChanged(ConnStateConnected) has normally
	// already set this; the CompareAndSwap covers a Kotlin implementation that
	// signals success only by returning.
	if c.gen.Load() == startGen {
		c.connected.CompareAndSwap(false, true)
	} else if !c.connected.Load() {
		return fmt.Errorf("android transport: connect to %q completed but the link had already dropped", deviceName)
	}

	c.devMu.Lock()
	if c.connectedAddr == "" && deviceAddress != "" {
		c.connectedAddr = deviceAddress
	}
	addr := c.connectedAddr
	d := c.devices[addr]
	c.devMu.Unlock()

	// Ordering barrier. BluetoothManager sets ConnectionStatusConnected
	// synchronously the moment this returns
	// (internal/core/bluetooth_manager.go:402). Without this wait, the
	// scanning/connecting phase events Kotlin raised DURING the connect would
	// still be sitting in the lifecycle lane and would be applied afterwards,
	// leaving the application showing "connecting" while it is connected.
	c.flushLifecycle()

	if d == nil || d.mfgHex == "" {
		// Not fatal, but it silently downgrades an Omni to a Home: DetectDeviceType
		// resolves an empty string to DeviceTypeHome.
		log.Printf("android transport: connected to %q [%s] with no advertisement record; "+
			"manufacturer data will be empty and device type detection will fall back to Home",
			deviceName, addr)
	}
	return nil
}

// Disconnect tears the link down.
//
// connected is cleared BEFORE crossing to Kotlin so that the
// OnConnectionStateChanged(ConnStateDisconnected) that follows is recognised as
// solicited and does not fire the connection-lost callback. This mirrors
// upstream's suppression (internal/core/tinygo_bluetooth_client.go:129-150)
// without upstream's mutex.
//
// Disconnect deliberately does NOT take gattMu: it is the cancel path, and waiting
// behind a connect attempt blocked in Kotlin would make it useless. Bridge
// implementations must be safe against this.
//
// Like upstream (internal/core/tinygo_bluetooth_client.go:384-386) it never errors
// merely because the link was already down: BluetoothManager calls it
// unconditionally on the teardown path (internal/core/bluetooth_manager.go:432, 440).
func (c *Client) Disconnect() error {
	// Clearing the state and bumping the generation first is what makes the
	// solicited callback silent AND stops an in-flight Connect from promoting.
	c.connected.Store(false)
	c.gen.Add(1)
	c.cancelling.Store(true)
	defer c.cancelling.Store(false)

	c.devMu.Lock()
	c.connectedAddr = ""
	c.devMu.Unlock()

	// Handlers are cleared here as well as in StopNotifications so that a packet
	// racing the teardown cannot reach a handler whose launch-monitor state has
	// already been reset.
	c.regMu.Lock()
	clear(c.handlers)
	c.regMu.Unlock()

	if c.closed.Load() || c.bridge == nil {
		return nil
	}
	if err := c.bridge.Disconnect(); err != nil {
		return bridgeErr(err, "disconnect", "")
	}
	return nil
}

// WriteCharacteristic writes to a characteristic and blocks until Kotlin reports
// the write complete. data is copied into a fresh Java byte[] by gobind, so Kotlin
// may retain it.
func (c *Client) WriteCharacteristic(uuid string, data []byte) error {
	if c.closed.Load() {
		return ErrClosed
	}
	if c.bridge == nil {
		return ErrNoBridge
	}
	if !c.connected.Load() {
		return errNotConnected
	}
	if !c.acquireGATT() {
		return ErrBusy
	}
	err := c.bridge.WriteCharacteristic(uuid, data)
	c.gattMu.Unlock()
	if err != nil {
		return bridgeErr(err, "write characteristic", uuid)
	}
	return nil
}

// ReadCharacteristic reads a characteristic and blocks until the value arrives.
// The returned slice is Go-owned (gobind copies interface return values).
func (c *Client) ReadCharacteristic(uuid string) ([]byte, error) {
	if c.closed.Load() {
		return nil, ErrClosed
	}
	if c.bridge == nil {
		return nil, ErrNoBridge
	}
	if !c.connected.Load() {
		return nil, errNotConnected
	}
	if !c.acquireGATT() {
		return nil, ErrBusy
	}
	data, err := c.bridge.ReadCharacteristic(uuid)
	c.gattMu.Unlock()
	if err != nil {
		return nil, bridgeErr(err, "read characteristic", uuid)
	}
	return data, nil
}

// StartNotifications registers handler for uuid and asks Kotlin to enable the
// characteristic's notifications.
//
// This is the first of the two signatures gomobile cannot carry. handler stays on
// this side of the boundary: it is filed in the registry and re-joined with its
// data in OnNotification. Kotlin never sees a Go func.
func (c *Client) StartNotifications(uuid string, handler func([]byte)) error {
	if c.closed.Load() {
		return ErrClosed
	}
	if handler == nil {
		return fmt.Errorf("android transport: nil notification handler for %s", uuid)
	}
	if c.bridge == nil {
		return ErrNoBridge
	}
	if !c.connected.Load() {
		return errNotConnected
	}

	// Registered before the radio is armed: Android can deliver the first packet
	// before the CCCD write is acknowledged, and an unregistered UUID is dropped.
	c.regMu.Lock()
	c.handlers[uuid] = handler
	c.regMu.Unlock()

	if !c.acquireGATT() {
		c.regMu.Lock()
		delete(c.handlers, uuid)
		c.regMu.Unlock()
		return ErrBusy
	}
	err := c.bridge.StartNotifications(uuid)
	c.gattMu.Unlock()
	if err != nil {
		c.regMu.Lock()
		delete(c.handlers, uuid)
		c.regMu.Unlock()
		return bridgeErr(err, "start notifications", uuid)
	}
	return nil
}

// StopNotifications unregisters uuid and asks Kotlin to disable the
// characteristic's notifications.
//
// The handler is dropped first, so a packet already in flight from the radio is
// discarded rather than delivered after teardown.
//
// It does not take gattMu: see the gattMu field comment. It also returns nil
// rather than "not connected" when the link is already down (a deliberate
// deviation from internal/core/tinygo_bluetooth_client.go:515-517) -- there is
// nothing to disable on a dead link, and the only caller treats the error as a log
// line (internal/core/bluetooth_manager.go:420-429).
func (c *Client) StopNotifications(uuid string) error {
	c.regMu.Lock()
	delete(c.handlers, uuid)
	c.regMu.Unlock()

	if c.closed.Load() || c.bridge == nil || !c.connected.Load() {
		return nil
	}
	if err := c.bridge.StopNotifications(uuid); err != nil {
		return bridgeErr(err, "stop notifications", uuid)
	}
	return nil
}

// IsConnected reports the cached link state. It costs one atomic load and never
// crosses the boundary.
func (c *Client) IsConnected() bool {
	return c.connected.Load()
}

// StartScan arms Kotlin's scanner and resets the discovered-device table, as
// upstream does (internal/core/tinygo_bluetooth_client.go:185).
//
// prefix is a NAME PREFIX -- BluetoothManager hardcodes "SquareGolf"
// (internal/core/bluetooth_manager.go:573). It is remembered and later handed to
// Bridge.Connect, so the transport never invents a device identity of its own.
//
// The table is cleared unconditionally: a record keyed by an address we are no
// longer attached to is worse than no record, because
// GetConnectedDeviceManufacturerData would then report the PREVIOUS device's hex
// and DetectDeviceType would resolve a Home unit as an Omni.
func (c *Client) StartScan(prefix string) error {
	if c.closed.Load() {
		return ErrClosed
	}
	if c.bridge == nil {
		return ErrNoBridge
	}

	c.devMu.Lock()
	c.devices = make(map[string]*discoveredDevice)
	c.devOrder = nil
	prev := c.scanPrefix
	c.scanPrefix = prefix
	c.devMu.Unlock()

	// State is armed before the Bridge call so a discovery arriving on a binder
	// thread the instant scanning starts is not filtered out.
	if err := c.bridge.StartScan(prefix); err != nil {
		c.devMu.Lock()
		c.scanPrefix = prev
		c.devMu.Unlock()
		return bridgeErr(err, "start scan", prefix)
	}
	return nil
}

// StopScan disarms Kotlin's scanner. Already-discovered devices are retained,
// matching upstream (internal/core/tinygo_bluetooth_client.go:222-233). It does
// not take gattMu -- it is a cancel path.
func (c *Client) StopScan() error {
	if c.closed.Load() {
		return ErrClosed
	}
	if c.bridge == nil {
		return ErrNoBridge
	}
	if err := c.bridge.StopScan(); err != nil {
		return bridgeErr(err, "stop scan", "")
	}
	return nil
}

// GetDiscoveredDevices returns the advertised names of the devices seen so far.
//
// This is the second signature gomobile cannot carry, and it never crosses the
// boundary: the table is filled one device at a time by OnDeviceDiscovered, so
// this is a pure Go read.
//
// Order is insertion order, not Go map order. Upstream ranges over a map
// (internal/core/tinygo_bluetooth_client.go:540) and therefore returns a different
// order on every call, which makes any UI built on it jump around.
func (c *Client) GetDiscoveredDevices() []string {
	c.devMu.Lock()
	defer c.devMu.Unlock()

	names := make([]string, 0, len(c.devOrder))
	for _, addr := range c.devOrder {
		if d := c.devices[addr]; d != nil && d.name != "" {
			names = append(names, d.name)
		}
	}
	return names
}

// GetConnectedDeviceName returns the advertised name of the connected device,
// falling back to the name Connect was asked for when Kotlin never reported an
// advertisement.
func (c *Client) GetConnectedDeviceName() string {
	c.devMu.Lock()
	defer c.devMu.Unlock()
	if d := c.devices[c.connectedAddr]; d != nil && d.name != "" {
		return d.name
	}
	return c.lastConnectName
}

// GetConnectedDeviceManufacturerData returns the hex of the connected device's
// first manufacturer-data record, exactly as upstream does
// (internal/core/tinygo_bluetooth_client.go:549-560).
//
// Kotlin performs the encoding, from
// ScanRecord.getManufacturerSpecificData().valueAt(0) -- the record PAYLOAD,
// without the two-byte company identifier, lowercase. Returning "" makes
// DetectDeviceType resolve every device as Home rather than Omni.
func (c *Client) GetConnectedDeviceManufacturerData() string {
	c.devMu.Lock()
	defer c.devMu.Unlock()
	if d := c.devices[c.connectedAddr]; d != nil {
		return d.mfgHex
	}
	return ""
}

// ---------------------------------------------------------------------------
// Capability interface: the two off-interface setters
// ---------------------------------------------------------------------------

// SetPhaseChangeCallback installs the scanning/connecting phase callback that
// internal/core/bluetooth_manager.go:61 wants to install.
func (c *Client) SetPhaseChangeCallback(callback func(core.ConnectionPhase)) {
	c.cbMu.Lock()
	c.onPhaseChange = callback
	c.cbMu.Unlock()
}

// SetConnectionLostCallback installs the unsolicited-disconnect callback that
// internal/core/bluetooth_manager.go:69 wants to install. It is driven from
// Kotlin's BluetoothGattCallback.onConnectionStateChange, relayed through
// OnConnectionStateChanged(ConnStateDisconnected).
func (c *Client) SetConnectionLostCallback(callback func()) {
	c.cbMu.Lock()
	c.onConnLost = callback
	c.cbMu.Unlock()
}

// ---------------------------------------------------------------------------
// Kotlin -> Go entry points
//
// Each of these arrives on an Android binder thread. They copy what they need,
// enqueue, and return. None of them runs upstream code inline.
// ---------------------------------------------------------------------------

// DispatchNotification delivers one notification packet from Kotlin.
//
// data ALIASES JNI memory that gobind releases the moment this function returns;
// it is copied before anything else happens. See the aliasing note in bridge.go.
func (c *Client) DispatchNotification(uuid string, data []byte) {
	if c.closed.Load() {
		return
	}

	// Cheap rejection before paying for the copy. The handler is looked up again
	// at delivery time so a StopNotifications racing this call still suppresses
	// the packet.
	c.regMu.RLock()
	_, registered := c.handlers[uuid]
	c.regMu.RUnlock()
	if !registered {
		return
	}

	buf := make([]byte, len(data))
	copy(buf, data)

	if !c.offer(c.notifyCh, event{kind: evNotification, uuid: uuid, data: buf}) {
		n := c.dropped.Add(1)
		log.Printf("android transport: notification queue full or shutting down, dropped a packet for %s (%d dropped total)", uuid, n)
	}
}

// OnDeviceDiscovered records one advertisement from Kotlin's scan callback.
//
// manufacturerDataHex may be empty on a scan response that carries no
// manufacturer record; an empty value never overwrites a non-empty one, because
// on Android the primary advertisement and the scan response for the same device
// arrive as separate callbacks and only one usually carries manufacturer data.
func (c *Client) OnDeviceDiscovered(name, address, manufacturerDataHex string) {
	if c.closed.Load() || address == "" {
		return
	}

	c.devMu.Lock()
	defer c.devMu.Unlock()

	// Backstop for Kotlin's own filter. Kotlin is expected to filter in its
	// ScanCallback; this only stops a mis-implemented filter from filling the
	// table with the room's Bluetooth traffic.
	if c.scanPrefix != "" && !strings.HasPrefix(name, c.scanPrefix) {
		return
	}

	if d := c.devices[address]; d != nil {
		if name != "" {
			d.name = name
		}
		if manufacturerDataHex != "" {
			d.mfgHex = manufacturerDataHex
		}
		return
	}
	if len(c.devOrder) >= maxDiscovered {
		return
	}
	c.devices[address] = &discoveredDevice{
		name:    name,
		address: address,
		mfgHex:  manufacturerDataHex,
	}
	c.devOrder = append(c.devOrder, address)
}

// OnConnectionStateChanged relays Kotlin's connection state. One entry point
// drives all four transitions: the two phase callbacks, the cached IsConnected
// value, and unsolicited-disconnect detection.
//
// detail is free-form diagnostic text (an Android GATT status, say). It is logged
// and never parsed.
func (c *Client) OnConnectionStateChanged(state int32, address, detail string) {
	if c.closed.Load() {
		return
	}

	switch state {
	case ConnStateScanning:
		c.offer(c.lifecycleCh, event{kind: evPhase, phase: core.PhaseScanning})

	case ConnStateConnecting:
		c.offer(c.lifecycleCh, event{kind: evPhase, phase: core.PhaseConnecting})

	case ConnStateConnected:
		if address != "" {
			c.devMu.Lock()
			c.connectedAddr = address
			c.devMu.Unlock()
		}
		c.connected.Store(true)

	case ConnStateDisconnected:
		// gen moves on every observed disconnect, whether or not it was solicited,
		// so a Connect blocked inside Kotlin can tell that its link died.
		c.gen.Add(1)
		c.devMu.Lock()
		c.connectedAddr = ""
		c.devMu.Unlock()
		// The CAS is the whole suppression rule: it succeeds exactly once per
		// true->false transition, so a solicited Disconnect (which already stored
		// false) is silent, and a duplicate callback fires nothing.
		if !c.connected.CompareAndSwap(true, false) {
			return
		}
		log.Printf("android transport: unsolicited disconnect from %s: %s", address, detail)
		c.offer(c.lifecycleCh, event{kind: evConnectionLost})

	default:
		log.Printf("android transport: unknown connection state %d from %s: %s", state, address, detail)
	}
}

// ---------------------------------------------------------------------------
// Delivery
// ---------------------------------------------------------------------------

// run drains one lane. Two independent lanes exist so a wedged notification
// handler cannot prevent a connection-loss event reaching the application.
func (c *Client) run(ch chan event) {
	defer c.wg.Done()
	for {
		select {
		case <-c.stop:
			return
		case ev := <-ch:
			if ev.kind == evFlush {
				close(ev.done)
				continue
			}
			c.deliver(ev)
		}
	}
}

// deliver runs one event's callback with no lock held. The recover is not
// decoration: upstream's handler chain parses raw device bytes
// (internal/core/launch_monitor.go:88-156) and a panic here would kill the lane
// goroutine, permanently deafening the app with no error anywhere.
func (c *Client) deliver(ev event) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("android transport: panic delivering event (kind %d, uuid %q): %v", ev.kind, ev.uuid, r)
		}
	}()

	switch ev.kind {
	case evNotification:
		c.regMu.RLock()
		handler := c.handlers[ev.uuid]
		c.regMu.RUnlock()
		if handler != nil {
			handler(ev.data)
		}

	case evPhase:
		c.cbMu.RLock()
		cb := c.onPhaseChange
		c.cbMu.RUnlock()
		if cb != nil {
			cb(ev.phase)
		}

	case evConnectionLost:
		c.cbMu.RLock()
		cb := c.onConnLost
		c.cbMu.RUnlock()
		if cb != nil {
			cb()
		}
	}
}

// flushLifecycle blocks until every lifecycle event queued before this call has
// been delivered. It is called from Connect, which already runs on a Go goroutine
// allowed to block; it is never called from a binder thread.
func (c *Client) flushLifecycle() {
	done := make(chan struct{})
	if !c.offer(c.lifecycleCh, event{kind: evFlush, done: done}) {
		return
	}
	timer := time.NewTimer(flushTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-c.stop:
	case <-timer.C:
		log.Println("android transport: timed out draining lifecycle events after connect")
	}
}

// offer enqueues ev, waiting at most enqueueGrace for space. It reports whether
// the event was accepted.
//
// The stop check comes first: after Close the buffered channel still accepts
// writes even though nothing drains it, and an event silently swallowed there
// would not be counted as dropped -- under-reporting loss at exactly the moment
// loss is happening.
//
// The fast path is then non-blocking. The bounded wait exists so a brief consumer
// stall does not cost a packet, and the bound exists so a long stall does not hold
// an Android binder thread: blocking one indefinitely stops the GATT callback the
// stalled handler is itself waiting for.
func (c *Client) offer(ch chan event, ev event) bool {
	select {
	case <-c.stop:
		return false
	default:
	}

	select {
	case ch <- ev:
		return true
	default:
	}

	timer := time.NewTimer(enqueueGrace)
	defer timer.Stop()
	select {
	case ch <- ev:
		return true
	case <-c.stop:
		return false
	case <-timer.C:
		return false
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// acquireGATT takes the single in-flight GATT slot, giving up after gattWait or as
// soon as a Disconnect starts. It reports whether the lock was taken; the caller
// unlocks gattMu on success.
//
// The bound matters: without it, a Bridge call stuck inside Kotlin would park
// every later operation forever, and no amount of Kotlin-side discipline on the
// stuck call can rescue the callers already queued behind it.
func (c *Client) acquireGATT() bool {
	if c.gattMu.TryLock() {
		return true
	}
	deadline := time.NewTimer(gattWait)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return false
		case <-deadline.C:
			return false
		case <-ticker.C:
			if c.cancelling.Load() {
				return false
			}
			if c.gattMu.TryLock() {
				return true
			}
		}
	}
}

// resolveTargetLocked picks a known device for a connect request, following
// upstream's matching order (internal/core/tinygo_bluetooth_client.go:274-282):
// exact name, else -- when no name was given -- the first device carrying the
// prefix upstream last scanned for. devMu must be held.
func (c *Client) resolveTargetLocked(deviceName, prefix string) *discoveredDevice {
	for _, addr := range c.devOrder {
		d := c.devices[addr]
		if d == nil {
			continue
		}
		if deviceName != "" && d.name == deviceName {
			return d
		}
		if deviceName == "" && prefix != "" && strings.HasPrefix(d.name, prefix) {
			return d
		}
	}
	return nil
}

// bridgeErr turns a Kotlin exception into a Go error with useful text.
//
// The incoming error is a gobind proxy holding a JNI global reference to the
// Throwable, and its Error() is Throwable.getMessage(), which is "" when the
// exception was constructed without a message
// (bind/java/seq_android.c.support:181-186). The message is flattened into a plain
// error rather than wrapped with %w: nothing upstream inspects these with
// errors.Is or errors.As, and flattening drops the reference to the Java object
// instead of pinning it for the lifetime of the error value.
func bridgeErr(err error, op, subject string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if msg == "" {
		msg = "kotlin threw an exception with no message"
	}
	if subject == "" {
		return fmt.Errorf("android transport: %s: %s", op, msg)
	}
	return fmt.Errorf("android transport: %s %s: %s", op, subject, msg)
}
