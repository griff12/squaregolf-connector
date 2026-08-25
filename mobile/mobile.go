// Package mobile is the gomobile FFI shim and nothing else. It flattens the Go
// engine's API into signatures gomobile can bind and translates callbacks into
// calls on Go interfaces that Kotlin implements. It holds no business logic:
// every function here is one line of marshalling over
// internal/transport/android.
//
// Every exported signature uses only gomobile-bindable types: numeric types,
// bool, string, []byte, error, and types declared in this package.
//
// INTEGER WIDTHS ARE DELIBERATE. gobind maps Go `int` to Java `long`, so every
// boundary-facing integer here is int32, which surfaces in Kotlin as Int. Do not
// "simplify" any of them to int.
//
// gobind mangles some names: GSProStatus becomes gsProStatus() and GSProError
// becomes gsProError() on the Java side. Cosmetic, but do not be surprised.
package mobile

import (
	"sync"

	androidtransport "github.com/brentyates/squaregolf-connector/internal/transport/android"
)

// ---------------------------------------------------------------------------
// Status codes, re-exported so Kotlin has one place to look.
// ---------------------------------------------------------------------------

const (
	StatusLMDisconnected = androidtransport.StatusLMDisconnected
	StatusLMScanning     = androidtransport.StatusLMScanning
	StatusLMConnecting   = androidtransport.StatusLMConnecting
	StatusLMConnected    = androidtransport.StatusLMConnected
	StatusLMError        = androidtransport.StatusLMError

	StatusGSProDisconnected = androidtransport.StatusGSProDisconnected
	StatusGSProConnecting   = androidtransport.StatusGSProConnecting
	StatusGSProConnected    = androidtransport.StatusGSProConnected
	StatusGSProError        = androidtransport.StatusGSProError

	StatusArmed    = androidtransport.StatusArmed
	StatusDisarmed = androidtransport.StatusDisarmed
)

// BLE connection states Kotlin reports through Connector.OnConnectionStateChanged.
const (
	ConnStateDisconnected = androidtransport.ConnStateDisconnected
	ConnStateScanning     = androidtransport.ConnStateScanning
	ConnStateConnecting   = androidtransport.ConnStateConnecting
	ConnStateConnected    = androidtransport.ConnStateConnected
)

// ---------------------------------------------------------------------------
// Kotlin-implemented interfaces
// ---------------------------------------------------------------------------

// StatusListener is implemented on the Kotlin side and handed to Start.
//
// EVERY METHOD RETURNS error, AND THAT IS LOAD-BEARING. gobind only emits the JNI
// ExceptionOccurred/ExceptionClear pair for an interface method that has a result
// (bind/genjava.go:1283-1289); for a result-less method it emits a bare
// CallVoidMethod. A Kotlin listener that throws from a result-less method leaves a
// pending JNI exception on the Go-owned attached thread, no Go panic is raised,
// and the next JNI operation trips ART's "called with pending exception" check ->
// JniAbort -> SIGABRT. With an error result the throw is caught and converted.
//
// Implementations must not block. They are invoked from a Go goroutine; post to
// the main looper and return.
type StatusListener interface {
	// OnStatus reports a lifecycle transition. code is one of the Status*
	// constants; detail carries the human-readable cause, or "".
	OnStatus(code int32, detail string) error

	// OnShot reports a shot's ball metrics, in the device's native units: metres
	// per second, degrees, RPM. UI only. Never log these above debug level.
	OnShot(ballSpeedMPS float64, launchAngleDeg float64, horizontalAngleDeg float64, totalSpinRPM int32, spinAxisDeg float64) error

	// OnLog surfaces an engine diagnostic. Go's own log output already reaches
	// Logcat as GoLog; this is for events the engine itself raises.
	OnLog(message string) error
}

// BleBridge is the bound mirror of androidtransport.Bridge. gobind turns it into a
// Java interface that Kotlin implements. Read that type's documentation before
// implementing this: it carries the full threading and timeout contract, and the
// rule that a []byte handed back to Go is released the moment the call returns.
type BleBridge interface {
	Connect(deviceName string, deviceAddress string, namePrefix string) error
	Disconnect() error
	StartScan(namePrefix string) error
	StopScan() error
	WriteCharacteristic(uuid string, data []byte) error
	ReadCharacteristic(uuid string) ([]byte, error)
	StartNotifications(uuid string) error
	StopNotifications(uuid string) error
}

// Structural identity guards. If either declaration drifts from its counterpart in
// internal/transport/android, this file stops compiling.
var (
	_ androidtransport.Bridge   = BleBridge(nil)
	_ androidtransport.Listener = StatusListener(nil)
)

// ---------------------------------------------------------------------------
// Connector: the single bound object Kotlin holds
// ---------------------------------------------------------------------------

// Connector is the whole bound surface. There is exactly one per process, because
// upstream's singletons cannot be rebuilt; NewConnector returns the same engine
// every time and only re-points the listener and endpoint.
type Connector struct {
	eng *androidtransport.Engine

	// snap is the discovered-device list captured by RefreshDiscoveredDevices and
	// indexed by DiscoveredDeviceNameAt. Guarded because gomobile invokes both from
	// whatever JVM thread calls them, and a torn slice header is an out-of-range
	// read inside Go -- a panic that kills the app process.
	snapMu sync.Mutex
	snap   []string
}

// NewConnector starts the engine and returns the bound handle.
//
// gsproHost and gsproPort come from Kotlin's persisted settings; there are no
// defaults in Go. 127.0.0.1 and 18921 are the values for this device.
//
// bridge may be nil, which selects simulator mode -- that is Phase 1. listener may
// be nil.
//
// This performs no I/O. Call ConnectDevice, wait for StatusLMConnected, then
// ConnectGSPro.
func NewConnector(gsproHost string, gsproPort int32, bridge BleBridge, simulateOmni bool, listener StatusListener) (*Connector, error) {
	cfg := androidtransport.Config{
		GSProHost:    gsproHost,
		GSProPort:    int(gsproPort),
		SimulateOmni: simulateOmni,
	}
	// A nil interface value arriving from Kotlin must be passed on as a nil
	// interface, not as a non-nil interface holding a nil value.
	if bridge != nil {
		cfg.Bridge = bridge
	}
	if listener != nil {
		cfg.Listener = listener
	}
	eng, err := androidtransport.Start(cfg)
	if err != nil {
		return nil, err
	}
	return &Connector{eng: eng}, nil
}

// SetListener re-points the status listener without touching the engine. Use it
// from Activity.onCreate when the process outlived the previous Activity. A nil
// listener installs a no-op.
func (c *Connector) SetListener(listener StatusListener) {
	if listener == nil {
		c.eng.SetListener(nil)
		return
	}
	c.eng.SetListener(listener)
}

// Stop parks both legs. It blocks for up to about 25 seconds; call it off the
// Android main thread, and only on real app teardown.
func (c *Connector) Stop() { c.eng.Stop() }

// IsRunning reports whether the engine is between Start and Stop.
func (c *Connector) IsRunning() bool { return c.eng.IsRunning() }

// Version returns the upstream connector version this AAR was built from.
func Version() string { return androidtransport.Version() }

// ---------------------------------------------------------------------------
// Launch-monitor leg
// ---------------------------------------------------------------------------

// ConnectDevice brings up the launch monitor. Asynchronous; watch OnStatus.
func (c *Connector) ConnectDevice() error { return c.eng.ConnectDevice() }

// DisconnectDevice drops the launch-monitor leg. Asynchronous.
func (c *Connector) DisconnectDevice() error { return c.eng.DisconnectDevice() }

// LaunchMonitorStatus returns "disconnected", "scanning", "connecting",
// "connected" or "error".
func (c *Connector) LaunchMonitorStatus() string { return c.eng.LaunchMonitorStatus() }

// RefreshDiscoveredDevices snapshots the discovered-device list and returns its
// length. Pair it with DiscoveredDeviceNameAt.
//
// Count+index rather than a joined string: a BLE LocalName is free-form UTF-8 from
// any nearby advertiser, so no separator is safe, and that name is the identity
// later passed back to ConnectDevice -- a split on an embedded separator is a
// connect failure, not a display glitch. An empty list also degrades correctly
// here (count 0), where "" split on a separator yields a one-element list holding
// an empty string.
func (c *Connector) RefreshDiscoveredDevices() int32 {
	names := c.eng.DiscoveredDeviceNames()
	c.snapMu.Lock()
	c.snap = names
	n := len(c.snap)
	c.snapMu.Unlock()
	return int32(n)
}

// DiscoveredDeviceNameAt indexes the snapshot taken by RefreshDiscoveredDevices.
// It returns "" out of range and never panics -- a Go panic across the FFI aborts
// the process.
func (c *Connector) DiscoveredDeviceNameAt(i int32) string {
	c.snapMu.Lock()
	defer c.snapMu.Unlock()
	if i < 0 || int(i) >= len(c.snap) {
		return ""
	}
	return c.snap[i]
}

// ---------------------------------------------------------------------------
// Kotlin -> Go BLE callbacks. No-ops in simulator mode.
// ---------------------------------------------------------------------------

// OnNotification delivers one BLE notification packet. data is copied on the Go
// side before it is queued, so Kotlin may reuse its buffer.
func (c *Connector) OnNotification(uuid string, data []byte) { c.eng.OnNotification(uuid, data) }

// OnDeviceDiscovered records one advertisement. manufacturerDataHex is the hex of
// ScanRecord.getManufacturerSpecificData().valueAt(0) -- the record payload,
// without the two-byte company identifier, lowercase. Returning "" makes upstream
// resolve every device as a Home unit rather than an Omni.
func (c *Connector) OnDeviceDiscovered(name string, address string, manufacturerDataHex string) {
	c.eng.OnDeviceDiscovered(name, address, manufacturerDataHex)
}

// OnConnectionStateChanged relays a BLE connection state. state is one of the
// ConnState* constants.
func (c *Connector) OnConnectionStateChanged(state int32, address string, detail string) {
	c.eng.OnConnectionStateChanged(state, address, detail)
}

// DroppedNotifications reports BLE packets discarded because Go's delivery queue
// was full. Non-zero means shot data was lost. Show it somewhere.
func (c *Connector) DroppedNotifications() int64 { return c.eng.DroppedNotifications() }

// ---------------------------------------------------------------------------
// GSPro leg
// ---------------------------------------------------------------------------

// ConnectGSPro dials the Open Connect endpoint. Asynchronous.
func (c *Connector) ConnectGSPro() error { return c.eng.ConnectGSPro() }

// DisconnectGSPro closes the GSPro connection. GSPconnect wedges its listener on a
// client FIN, so treat this as user-initiated, not routine cleanup.
func (c *Connector) DisconnectGSPro() error { return c.eng.DisconnectGSPro() }

// GSProStatus returns "disconnected", "connecting", "connected" or "error". Bound
// to Kotlin as gsProStatus().
func (c *Connector) GSProStatus() string { return c.eng.GSProStatus() }

// GSProError returns the last GSPro error text, or "". Bound as gsProError().
func (c *Connector) GSProError() string { return c.eng.GSProError() }

// ---------------------------------------------------------------------------
// Ball-detection gate
// ---------------------------------------------------------------------------

// Arm activates ball detection. Requires the launch monitor to be connected.
func (c *Connector) Arm() error { return c.eng.Arm() }

// Disarm stops ball detection. It cannot stop GSPro from arming a new cycle: the
// connectapi plugin re-arms on every "GSPro ready" message. Disconnect the GSPro
// leg if the launch monitor must stay quiet.
func (c *Connector) Disarm() error { return c.eng.Disarm() }

// IsArmed reports what Arm and Disarm last did. It is not a guarantee that the
// launch monitor is idle.
func (c *Connector) IsArmed() bool { return c.eng.IsArmed() }
