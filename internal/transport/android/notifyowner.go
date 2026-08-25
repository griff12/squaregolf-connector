package android

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/brentyates/squaregolf-connector/internal/core"
)

// notifyOwner is a core.BluetoothClient decorator that takes ownership of the
// notification-handler table away from the wrapped client.
//
// It holds NO protocol knowledge: no UUID literals, no opcode inspection, no
// packet parsing. Every UUID it sees arrives from upstream and is passed through
// opaquely. It is therefore correct in front of any BluetoothClient, and it is
// installed unconditionally -- in front of the simulator in Phase 1 and in front
// of the Kotlin-backed Client in Phase 2.
//
// # Why it exists
//
// Two reproducible process-killers live in upstream's simulator, and both are
// caused by the handler map being mutated after the device is live.
//
//  1. FATAL: concurrent map read and map write.
//     internal/core/simulator_mock.go reads s.notifyHandlers WITHOUT the lock at
//     :453, :469, :565, :589, :607 and :650 -- on the processCommands goroutine
//     and on the simulateBallDetection goroutine -- while StartNotifications
//     (:238) and StopNotifications (:256) write it under the lock. Any handler
//     registration that overlaps a command in flight trips Go's runtime map guard.
//     That is a fatal throw, not a panic: no recover, no notifyCallbacks recover,
//     and no safeListener can catch it, and it fires in a plain release build
//     with no -race.
//
//  2. PANIC: nil handler dereference.
//     simulateBallDetection calls the handler UNGUARDED at
//     internal/core/simulator_mock.go:566 and :590. Two windows expose it:
//     - CONNECT SIDE: connectDevice sets the client connected and then performs
//     five characteristic reads -- roughly four seconds, because the simulator
//     has no serial-number characteristic and ReadSerialNumber retries five
//     times with one-second sleeps -- BEFORE it calls EnableNotifications
//     (internal/core/bluetooth_manager.go:352-397). Anything arming ball
//     detection inside that window starts a goroutine that dereferences nil.
//     - DISCONNECT SIDE: disconnectDevice calls StopNotifications, which DELETES
//     the handler, before Disconnect (internal/core/bluetooth_manager.go:418-430),
//     while a detection cycle may still be running.
//     The connectapi plugin arms on every "GSPro ready" and player-information
//     message (internal/plugins/connectapi/integration.go:200-224), unprompted, so
//     both windows are hit in ordinary use rather than under stress.
//
// # How it closes them
//
//   - The wrapped client's handler table is written AT MOST ONCE PER UUID, ever.
//     The first StartNotifications for a UUID installs a permanent fan-out closure
//     downstream; every later StartNotifications for that UUID only updates this
//     decorator's own map. StopNotifications never touches the wrapped client at
//     all. The wrapped client therefore never sees a delete and never sees a
//     second write, so neither the map race nor the nil dereference can occur --
//     the fan-out closure is always present, and it no-ops when this decorator has
//     no handler registered.
//
//   - Writes are refused until notifications have been armed at least once. That
//     converts the connect-side window from a panic inside a detached goroutine
//     into an error returned to the caller, which upstream logs. Nothing upstream
//     writes before EnableNotifications: connectDevice performs reads only
//     (internal/core/bluetooth_manager.go:263, 292, 307), and the heartbeat ticker
//     no-ops while a write fails.
//
// The decorator forwards the phase/connection-lost capability setters to the
// wrapped client, because BluetoothManager asserts against the value it was
// handed (internal/core/bluetooth_manager.go:60) and that value is this
// decorator. Forwarding is a silent no-op for a client that does not support them
// -- the simulator -- which is exactly today's behaviour there.
type notifyOwner struct {
	inner core.BluetoothClient

	// armed reports whether any notification has ever been enabled. It is never
	// cleared: once the fan-out closures are installed downstream they stay
	// installed for the process lifetime, so the hazard cannot return.
	armed atomic.Bool

	mu       sync.RWMutex
	handlers map[string]func([]byte)
	seeded   map[string]bool
}

var _ core.BluetoothClient = (*notifyOwner)(nil)

// ErrNotificationsNotArmed is returned by WriteCharacteristic before any
// notification has been enabled. Upstream logs it; it is not fatal.
var ErrNotificationsNotArmed = errors.New(
	"android transport: refusing to write before notifications are armed")

// OwnNotifications wraps client so that the handler table is owned here rather
// than by the client. See notifyOwner for why this is not optional.
func OwnNotifications(client core.BluetoothClient) core.BluetoothClient {
	return &notifyOwner{
		inner:    client,
		handlers: make(map[string]func([]byte)),
		seeded:   make(map[string]bool),
	}
}

// dispatch is the permanent fan-out installed downstream for one UUID. It copies
// the handler out from under the read lock before calling it: a handler can block
// for ten seconds inside LaunchMonitor.SendCommand, and holding an RLock across
// that would stall every later registration -- and would deadlock outright if the
// handler ever registered a notification of its own.
func (n *notifyOwner) dispatch(uuid string) func([]byte) {
	return func(data []byte) {
		n.mu.RLock()
		h := n.handlers[uuid]
		n.mu.RUnlock()
		if h != nil {
			h(data)
		}
	}
}

// StartNotifications registers handler for uuid. It forwards to the wrapped
// client only the first time it sees a given UUID.
func (n *notifyOwner) StartNotifications(uuid string, handler func([]byte)) error {
	if handler == nil {
		return errors.New("android transport: nil notification handler")
	}

	n.mu.Lock()
	n.handlers[uuid] = handler
	alreadySeeded := n.seeded[uuid]
	n.mu.Unlock()

	if alreadySeeded {
		n.armed.Store(true)
		return nil
	}

	if err := n.inner.StartNotifications(uuid, n.dispatch(uuid)); err != nil {
		n.mu.Lock()
		delete(n.handlers, uuid)
		n.mu.Unlock()
		return err
	}

	n.mu.Lock()
	n.seeded[uuid] = true
	n.mu.Unlock()
	n.armed.Store(true)
	return nil
}

// StopNotifications drops the handler here and NEVER forwards. Forwarding would
// delete the wrapped client's map entry, which is exactly the mutation that makes
// upstream's simulator fatal. A packet arriving after this call finds no handler
// and is discarded, which is the behaviour the caller wanted.
func (n *notifyOwner) StopNotifications(uuid string) error {
	n.mu.Lock()
	delete(n.handlers, uuid)
	n.mu.Unlock()
	return nil
}

// WriteCharacteristic refuses until notifications have been armed. See the type
// comment: this is what turns the connect-side arm window from a process kill
// into a logged error.
func (n *notifyOwner) WriteCharacteristic(uuid string, data []byte) error {
	if !n.armed.Load() {
		return ErrNotificationsNotArmed
	}
	return n.inner.WriteCharacteristic(uuid, data)
}

// Disconnect clears the handlers so nothing is delivered across a teardown, then
// forwards. It does not forward StopNotifications first -- upstream's caller
// already did that, and forwarding is precisely what this type exists to prevent.
func (n *notifyOwner) Disconnect() error {
	n.mu.Lock()
	clear(n.handlers)
	n.mu.Unlock()
	return n.inner.Disconnect()
}

// SetPhaseChangeCallback forwards to the wrapped client when it supports the
// capability. BluetoothManager type-asserts against the value it was handed --
// this decorator -- so without these two methods a Client behind the decorator
// would silently lose phase reporting and unsolicited-disconnect detection.
func (n *notifyOwner) SetPhaseChangeCallback(callback func(core.ConnectionPhase)) {
	if p, ok := n.inner.(interface {
		SetPhaseChangeCallback(func(core.ConnectionPhase))
	}); ok {
		p.SetPhaseChangeCallback(callback)
	}
}

// SetConnectionLostCallback forwards to the wrapped client when it supports the
// capability.
func (n *notifyOwner) SetConnectionLostCallback(callback func()) {
	if p, ok := n.inner.(interface{ SetConnectionLostCallback(func()) }); ok {
		p.SetConnectionLostCallback(callback)
	}
}

var _ CallbackCapableClient = (*notifyOwner)(nil)

// Everything below is a pass-through.

func (n *notifyOwner) Connect(deviceName, deviceAddress string) error {
	return n.inner.Connect(deviceName, deviceAddress)
}

func (n *notifyOwner) ReadCharacteristic(uuid string) ([]byte, error) {
	return n.inner.ReadCharacteristic(uuid)
}

func (n *notifyOwner) IsConnected() bool { return n.inner.IsConnected() }

func (n *notifyOwner) StartScan(prefix string) error { return n.inner.StartScan(prefix) }

func (n *notifyOwner) StopScan() error { return n.inner.StopScan() }

func (n *notifyOwner) GetDiscoveredDevices() []string { return n.inner.GetDiscoveredDevices() }

func (n *notifyOwner) GetConnectedDeviceName() string { return n.inner.GetConnectedDeviceName() }

func (n *notifyOwner) GetConnectedDeviceManufacturerData() string {
	return n.inner.GetConnectedDeviceManufacturerData()
}
