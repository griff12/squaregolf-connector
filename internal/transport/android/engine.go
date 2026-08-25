package android

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brentyates/squaregolf-connector/internal/core"
	"github.com/brentyates/squaregolf-connector/internal/plugin"
	"github.com/brentyates/squaregolf-connector/internal/plugins/connectapi"
	"github.com/brentyates/squaregolf-connector/internal/version"
)

// Engine composes upstream's singletons into a runnable application and owns its
// lifecycle. It lives here rather than in mobile/ because CLAUDE.md requires
// mobile/ to be marshalling only; every method below is called by exactly one
// one-line wrapper in mobile/.
//
// # Status codes
//
// Two independent legs are reported through one code space so Kotlin can write a
// single exhaustive `when`. int32 rather than Go's int: gobind maps Go int to
// Java long, and these are enum-shaped values that should surface as Int.
const (
	// Launch-monitor leg. Mirrors core.ConnectionStatus.
	StatusLMDisconnected int32 = 0
	StatusLMScanning     int32 = 1
	StatusLMConnecting   int32 = 2
	StatusLMConnected    int32 = 3
	StatusLMError        int32 = 4

	// GSPro Open Connect leg. Mirrors plugin.Status as reported through
	// StateManager.SetIntegrationStatus under the key "gspro".
	StatusGSProDisconnected int32 = 10
	StatusGSProConnecting   int32 = 11
	StatusGSProConnected    int32 = 12
	StatusGSProError        int32 = 13

	// Ball-detection gate.
	StatusArmed    int32 = 20
	StatusDisarmed int32 = 21
)

// Listener receives lifecycle events. mobile/ declares a structurally identical
// bound interface (mobile.StatusListener) that Kotlin implements.
//
// EVERY METHOD RETURNS error, AND THAT IS LOad-BEARING, NOT STYLE. gobind only
// emits the JNI ExceptionOccurred/ExceptionClear pair for an interface method
// whose Go signature has a result (bind/genjava.go:1283-1289). For a method with
// no result it emits a bare CallVoidMethod. So a Kotlin listener that throws from
// a result-less method leaves a PENDING JNI exception on the Go-owned attached
// thread; no Go panic is raised, recover() never runs, and the next JNI operation
// on that thread trips ART's "called with pending exception" check -> JniAbort ->
// SIGABRT. With an error result the exception is caught and converted, and Go can
// log and continue.
//
// Implementations must not block: post to the main looper and return.
type Listener interface {
	// OnStatus reports a lifecycle transition. code is one of the Status*
	// constants; detail carries the human-readable cause, or "".
	OnStatus(code int32, detail string) error

	// OnShot reports that a shot's ball metrics reached the GSPro plugin. Values
	// are in the device's native units: metres per second, degrees, RPM. This is
	// for the UI only -- nothing downstream depends on it.
	//
	// Per CLAUDE.md, never log these above debug level.
	OnShot(ballSpeedMPS float64, launchAngleDeg float64, horizontalAngleDeg float64, totalSpinRPM int32, spinAxisDeg float64) error

	// OnLog surfaces an engine-originated diagnostic. Go's own log output already
	// reaches Logcat as GoLog; this is for events the engine itself raises.
	OnLog(message string) error
}

// Config is what the caller must supply to Start.
type Config struct {
	// GSProHost and GSProPort address GSPro's Open Connect listener. There are no
	// defaults here on purpose: Kotlin owns the persisted settings (127.0.0.1 and
	// 18921 on this device). internal/config is deliberately NOT imported -- it
	// calls os.UserHomeDir, os.MkdirAll and os.ReadFile at construction, all of
	// which fail or misbehave in an Android app process, and it still defaults
	// GSProPort to the pre-Phase-0 value 921.
	GSProHost string
	GSProPort int

	// Bridge is the Kotlin BLE implementation. When nil the engine runs in
	// simulator mode against upstream's SimulatorBluetoothClient, which is Phase 1.
	Bridge Bridge

	// SimulateOmni makes the simulator present as an Omni rather than a Home.
	// Ignored when Bridge is non-nil.
	SimulateOmni bool

	// Listener receives status, shot and log events. May be nil.
	Listener Listener
}

const (
	// gsproPluginName is the registry key connectapi.OpenAPI() registers under
	// (internal/plugins/connectapi/integration.go:34). It is how we reach the
	// plugin's Connectable capability and how we filter its status reports.
	gsproPluginName = "gspro"

	// simulatedDeviceSuffix is appended to upstream's own device prefix to label
	// the simulated device. SimulatorBluetoothClient.Connect stores the name
	// verbatim and never scans, so this is a display name, not a filter.
	simulatedDeviceSuffix = "(SIM)"

	// dispatchQueueDepth bounds the listener-callback queue.
	dispatchQueueDepth = 128

	// stopWait bounds Stop's wait for the launch-monitor disconnect. It must
	// exceed the worst case: BluetoothManager runs the pre-disconnect hook first
	// (internal/core/bluetooth_manager.go:196), which is
	// LaunchMonitor.DeactivateBallDetection -> SendCommand, itself bounded at five
	// seconds to enqueue plus five to execute
	// (internal/core/launch_monitor.go:657-669).
	stopWait = 20 * time.Second
)

var (
	engineMu sync.Mutex
	engine   *Engine
)

// Engine is the composed application. Exactly one exists per process, because
// core.GetInstance, core.GetBluetoothInstance and core.GetLaunchMonitorInstance
// are sync.Once singletons that never reset.
type Engine struct {
	sm       *core.StateManager
	bm       *core.BluetoothManager
	lm       *core.LaunchMonitor
	registry *plugin.Registry

	// client is the Kotlin-backed transport, nil in simulator mode.
	client *Client
	// sim is upstream's simulator, nil when a Bridge was supplied.
	sim *core.SimulatorBluetoothClient
	// simulatorMode records which of the two above is installed, so Disarm can
	// branch on a fact rather than on a field that is always populated.
	simulatorMode bool

	// listener is swapped when an Activity is recreated. atomic.Pointer to the
	// interface type, never atomic.Value: storing two different concrete types
	// into one atomic.Value panics with "store of inconsistently typed value",
	// which is exactly what happens when Kotlin passes a real listener once and
	// null the next time.
	listener atomic.Pointer[Listener]

	// armed is the engine's own view of the ball-detection gate: what Arm and
	// Disarm last did. It is NOT the whole truth -- the connectapi plugin arms
	// independently on every "GSPro ready" message and the engine has no veto.
	armed atomic.Bool

	// running reports whether the engine is between Start and Stop.
	running atomic.Bool

	// lastLMError carries the detail for a StatusLMError report, because
	// StateManager delivers SetLastError and SetConnectionStatus separately.
	lastLMError atomic.Pointer[string]

	// lmDisconnected lets Stop observe the end of BluetoothManager's asynchronous
	// disconnect instead of returning while it is still in flight.
	lmDisconnected chan struct{}

	// dispatchCh serialises every crossing into the listener onto one goroutine.
	// This is not tidiness. Upstream calls state callbacks synchronously from
	// whatever goroutine changed the state, sometimes holding a lock:
	// simulator.Base.Connect holds ConnectMutex across SetStatus(Connecting),
	// SetStatus(Connected) and OnConnected (internal/core/simulator/connection.go:70,
	// 105, 106), and OnConnected reaches ActivateBallDetection -> SendCommand,
	// which blocks up to ten seconds. Calling into Kotlin from there blocks the
	// reconnect engine and deadlocks outright if the Kotlin listener touches the
	// main thread while the main thread is inside another engine call.
	dispatchCh chan dispatchItem

	endpointMu sync.Mutex
	gsproHost  string
	gsproPort  int
}

type dispatchItem struct {
	kind    int
	code    int32
	detail  string
	message string
	shot    shotValues
}

type shotValues struct {
	ballSpeedMPS       float64
	launchAngleDeg     float64
	horizontalAngleDeg float64
	totalSpinRPM       int32
	spinAxisDeg        float64
}

const (
	dispatchStatus = iota
	dispatchLog
	dispatchShot
)

// Start builds the engine on first call and attaches the listener. It performs no
// I/O: it neither dials GSPro nor connects the launch monitor. Call ConnectDevice
// and ConnectGSPro afterwards, in that order.
//
// A second and later call only re-points the listener and updates the endpoint.
// Rebuilding is impossible -- upstream's singletons never reset -- and re-running
// the wiring would be actively harmful: SetupNotifications APPENDS to the state
// callback lists with no unregister (internal/core/state_manager.go:374-378), and
// registry.StartAll would replace a live simulator.Base and re-subscribe
// OnBallMetrics, sending every shot to GSPro twice.
func Start(cfg Config) (*Engine, error) {
	if cfg.GSProHost == "" {
		return nil, errors.New("android engine: GSProHost must not be empty")
	}
	if cfg.GSProPort < 1 || cfg.GSProPort > 65535 {
		return nil, fmt.Errorf("android engine: GSProPort %d out of range 1-65535", cfg.GSProPort)
	}

	e, fresh := attach(cfg)
	e.running.Store(true)

	// Everything below runs with engineMu released, so a listener may call back
	// into this package without deadlocking.
	if !fresh {
		e.log("engine already running; listener reattached")
		e.emit(lmStatusCode(e.sm.GetConnectionStatus()), "")
		e.emit(gsproStatusCode(e.sm.GetIntegrationStatus(gsproPluginName).Status), "")
		return e, nil
	}

	e.log("engine started")
	e.emit(StatusLMDisconnected, "")
	e.emit(StatusGSProDisconnected, "")
	e.emit(StatusDisarmed, "")
	return e, nil
}

// Current returns the process engine, or an error if Start has never run.
func Current() (*Engine, error) {
	engineMu.Lock()
	defer engineMu.Unlock()
	if engine == nil {
		return nil, errors.New("android engine: Start has not been called")
	}
	return engine, nil
}

// attach returns the process engine, building it on first call. fresh reports
// whether this call was the one that built it.
func attach(cfg Config) (e *Engine, fresh bool) {
	engineMu.Lock()
	defer engineMu.Unlock()

	if engine != nil {
		engine.SetListener(cfg.Listener)
		engine.setEndpoint(cfg.GSProHost, cfg.GSProPort)
		return engine, false
	}

	e = &Engine{
		lmDisconnected: make(chan struct{}, 1),
		dispatchCh:     make(chan dispatchItem, dispatchQueueDepth),
	}
	e.SetListener(cfg.Listener)
	e.setEndpoint(cfg.GSProHost, cfg.GSProPort)
	empty := ""
	e.lastLMError.Store(&empty)
	go e.runDispatch()

	// --- Init order. Every step depends on the one before it. ---

	// 1. StateManager first: BluetoothManager, LaunchMonitor and the plugin host
	//    are all constructed around it.
	e.sm = core.GetInstance()

	// 2. Build the BluetoothClient BEFORE the manager is told about it, and wrap
	//    it so this package owns the notification-handler table. See notifyOwner:
	//    the wrapper is what stops upstream's simulator from taking the process
	//    down with a nil handler dereference or a concurrent map write.
	var client core.BluetoothClient
	if cfg.Bridge != nil {
		e.client = NewClient(cfg.Bridge)
		client = e.client
	} else {
		// ResponseDelay is deliberately left at zero: the simulator sleeps it on
		// every read, write and notify-subscribe, and connectDevice performs five
		// characteristic reads -- one a five-attempt retry with one-second sleeps,
		// because the simulator has no serial-number characteristic -- before it
		// enables notifications. All of that must fit inside the simulator's
		// ten-second inactivity window.
		e.sim = core.NewSimulatorBluetoothClient(core.SimulatorConfig{
			BatteryDrainRate: 1,
			SimulateOmni:     cfg.SimulateOmni,
		})
		e.simulatorMode = true
		client = e.sim
	}

	// 3. BluetoothManager singleton.
	e.bm = core.GetBluetoothInstance(e.sm)

	// 4. SetClient MUST precede GetLaunchMonitorInstance.
	//    GetLaunchMonitorInstance snapshots btManager.GetClient() inside its
	//    sync.Once (internal/core/launch_monitor.go:21-30) into an atomic.Pointer
	//    that only UpdateBluetoothClient rewrites, and nothing outside tests calls
	//    that. Take the LaunchMonitor first and it holds a nil client for the life
	//    of the process: every SendCommand, heartbeat tick and ActivateBallDetection
	//    then fails "not connected to device" forever, unrecoverable short of
	//    restarting the app.
	e.bm.SetClient(OwnNotifications(client))

	// 5. LaunchMonitor singleton -- now snapshots a live client.
	e.lm = core.GetLaunchMonitorInstance(e.sm, e.bm)

	// 6. SetupNotifications wires the notification handler, the pre-disconnect
	//    hook and the LaunchMonitor's own state observers, and starts the
	//    five-second heartbeat ticker (internal/core/launch_monitor.go:866-952). It
	//    must run before any connect: connectDevice calls EnableNotifications,
	//    which fails outright if no handler is registered
	//    (internal/core/bluetooth_manager.go:222-225) and that error aborts the
	//    connect.
	e.lm.SetupNotifications(e.bm)

	// 7. Plugin registry, then the GSPro Open Connect plugin. connectapi.New only
	//    records the endpoint and Integration.Start only builds the transport and
	//    subscribes to metrics. Neither dials -- the dial is BeginConnect, driven
	//    from ConnectGSPro.
	e.registry = plugin.NewRegistry(core.NewPluginHost(e.sm, e.lm))
	e.registry.Register(connectapi.New(connectapi.OpenAPI(), cfg.GSProHost, cfg.GSProPort))
	e.registry.StartAll(context.Background())

	// 8. Our observers last, so connectapi's metric subscription from step 7 runs
	//    first and GSPro gets the shot before the UI does.
	e.registerObservers()

	engine = e
	return e, true
}

// SetListener re-points the status listener. Use it from Activity.onCreate when
// the process outlived the previous Activity. A nil listener is replaced with a
// no-op.
func (e *Engine) SetListener(l Listener) {
	if l == nil {
		l = noopListener{}
	}
	safe := Listener(safeListener{inner: l})
	e.listener.Store(&safe)
}

// Stop disarms ball detection, parks the GSPro TCP leg and disconnects the launch
// monitor. It does NOT destroy the engine -- upstream's singletons cannot be
// destroyed -- so a later Start reattaches to the same graph and ConnectDevice /
// ConnectGSPro bring both legs back up.
//
// It blocks for up to stopWait plus the plugin's own shutdown: simulator.Base.Stop
// waits on its reader and reconnect goroutines. CALL IT OFF THE ANDROID MAIN
// THREAD, and only on real app teardown -- closing the TCP leg makes GSPconnect
// hot-spin on the FIN and wedge its listener until it is restarted
// (PHASE0-FINDINGS).
func (e *Engine) Stop() {
	e.armed.Store(false)
	if err := e.stopBallDetection(); err != nil {
		e.log("stop: deactivate ball detection: " + err.Error())
	}
	e.emit(StatusDisarmed, "")

	e.registry.StopAll()

	// BluetoothManager.DisconnectBluetooth does its work on a goroutine
	// (internal/core/bluetooth_manager.go:186), so returning here would leave a
	// disconnect in flight that could land after a later ConnectDevice and knock
	// the new session straight back to disconnected. Drain any stale signal, then
	// wait for this one. Bounded, and never a bare sleep.
	select {
	case <-e.lmDisconnected:
	default:
	}
	e.bm.DisconnectBluetooth()

	timer := time.NewTimer(stopWait)
	defer timer.Stop()
	select {
	case <-e.lmDisconnected:
	case <-timer.C:
		e.log("stop: timed out waiting for launch monitor disconnect")
	}

	// The edge may have been missed if the status was already disconnected;
	// confirm quiescence rather than trusting a single transition.
	if e.sm.GetConnectionStatus() != core.ConnectionStatusDisconnected {
		e.log("stop: launch monitor did not reach disconnected state")
	}

	e.running.Store(false)
	e.log("engine stopped")
}

// IsRunning reports whether the engine is between Start and Stop.
func (e *Engine) IsRunning() bool { return e.running.Load() }

// Version returns the upstream connector version this build was made from.
func Version() string { return version.GetShortVersion() }

// ---------------------------------------------------------------------------
// Launch-monitor leg
// ---------------------------------------------------------------------------

// ConnectDevice brings up the launch monitor. It returns as soon as the attempt
// is queued: StartBluetoothConnection does the rest on its own goroutine and
// reports progress through OnStatus.
//
// Connect this leg BEFORE the GSPro leg. GSPro announces "GSPro ready" unprompted
// and the connectapi plugin arms ball detection the moment it arrives, so bringing
// the GSPro leg up first aims a command at a launch monitor that is not ready.
func (e *Engine) ConnectDevice() error {
	name := ""
	if e.simulatorMode {
		name = core.BluetoothDevicePrefix + simulatedDeviceSuffix
	}
	e.bm.StartBluetoothConnection(name, "")
	return nil
}

// DisconnectDevice drops the launch-monitor leg, leaving GSPro connected.
// Asynchronous, like ConnectDevice.
func (e *Engine) DisconnectDevice() error {
	e.armed.Store(false)
	e.bm.DisconnectBluetooth()
	return nil
}

// LaunchMonitorStatus returns "disconnected", "scanning", "connecting",
// "connected" or "error".
func (e *Engine) LaunchMonitorStatus() string {
	return string(e.sm.GetConnectionStatus())
}

// DiscoveredDeviceNames returns the advertised names seen so far, in discovery
// order. Empty in simulator mode until a scan has run.
func (e *Engine) DiscoveredDeviceNames() []string {
	client := e.bm.GetClient()
	if client == nil {
		return nil
	}
	return client.GetDiscoveredDevices()
}

// ---------------------------------------------------------------------------
// Kotlin -> Go transport callbacks. No-ops in simulator mode.
// ---------------------------------------------------------------------------

// OnNotification delivers one BLE notification packet from Kotlin.
func (e *Engine) OnNotification(uuid string, data []byte) {
	if e.client != nil {
		e.client.DispatchNotification(uuid, data)
	}
}

// OnDeviceDiscovered records one advertisement from Kotlin's scan callback.
func (e *Engine) OnDeviceDiscovered(name, address, manufacturerDataHex string) {
	if e.client != nil {
		e.client.OnDeviceDiscovered(name, address, manufacturerDataHex)
	}
}

// OnConnectionStateChanged relays Kotlin's BLE connection state.
func (e *Engine) OnConnectionStateChanged(state int32, address, detail string) {
	if e.client != nil {
		e.client.OnConnectionStateChanged(state, address, detail)
	}
}

// DroppedNotifications reports inbound BLE packets discarded because the delivery
// queue was full. Non-zero means shot data was lost; surface it.
func (e *Engine) DroppedNotifications() int64 {
	if e.client == nil {
		return 0
	}
	return e.client.DroppedNotifications()
}

// ---------------------------------------------------------------------------
// GSPro leg
// ---------------------------------------------------------------------------

// ConnectGSPro dials the Open Connect endpoint supplied to Start and enables the
// plugin's exponential-backoff reconnect engine. It returns immediately; the dial
// has a five-second timeout and runs on its own goroutine.
func (e *Engine) ConnectGSPro() error {
	c, ok := e.registry.Connectable(gsproPluginName)
	if !ok {
		return errors.New("android engine: gspro plugin is not connectable")
	}
	host, port := e.endpoint()
	go c.BeginConnect(host, port)
	return nil
}

// DisconnectGSPro closes the GSPro connection and disables auto-reconnect.
//
// GSPconnect wedges its listener on a client FIN, so treat this as a
// user-initiated action, not routine lifecycle cleanup.
func (e *Engine) DisconnectGSPro() error {
	c, ok := e.registry.Connectable(gsproPluginName)
	if !ok {
		return errors.New("android engine: gspro plugin is not connectable")
	}
	go c.EndConnect()
	return nil
}

// GSProStatus returns "disconnected", "connecting", "connected" or "error".
func (e *Engine) GSProStatus() string {
	return e.sm.GetIntegrationStatus(gsproPluginName).Status
}

// GSProError returns the plugin's last reported error text, or "".
func (e *Engine) GSProError() string {
	return e.sm.GetIntegrationStatus(gsproPluginName).Error
}

// ---------------------------------------------------------------------------
// Ball-detection gate
// ---------------------------------------------------------------------------

// Arm activates ball detection.
//
// Against the simulator one Arm produces exactly one shot cycle, about 11.5
// seconds long, after which simulateBallDetection sets the device back to idle
// itself (internal/core/simulator_mock.go:634) and its goroutine exits. The stream
// continues only while GSPro is connected, because the connectapi plugin re-arms
// on every "GSPro ready" and player-information message
// (internal/plugins/connectapi/integration.go:200-224). That is what makes it
// free-run, and it is outside this engine's control.
//
// Arm refuses unless the launch monitor is connected. connectDevice enables
// notifications before it sets ConnectionStatusConnected
// (internal/core/bluetooth_manager.go:396-402), so this is the right gate for our
// own call path; the notifyOwner write-gate covers the plugin's path, which we
// cannot gate.
func (e *Engine) Arm() error {
	if e.sm.GetConnectionStatus() != core.ConnectionStatusConnected {
		return errors.New("android engine: launch monitor is not connected")
	}
	e.armed.Store(true)
	if err := e.lm.ActivateBallDetection(); err != nil {
		e.armed.Store(false)
		return err
	}
	e.emit(StatusArmed, "")
	return nil
}

// Disarm stops ball detection.
//
// It stops the cycle in flight. It cannot stop GSPro from arming a new one: the
// connectapi plugin calls ActivateBallDetection on every "GSPro ready" message and
// this engine has no veto -- a veto was built and measured, and it killed every
// legitimate plugin-driven cycle, because `armed` tracks only our own Arm/Disarm.
// Disconnect the GSPro leg if the launch monitor must stay quiet.
func (e *Engine) Disarm() error {
	e.armed.Store(false)
	err := e.stopBallDetection()
	e.emit(StatusDisarmed, "")
	return err
}

// IsArmed reports the engine's own view of the gate -- what Arm and Disarm last
// did. The connectapi plugin arms independently, so false does not guarantee the
// launch monitor is idle.
func (e *Engine) IsArmed() bool { return e.armed.Load() }

// stopBallDetection is the one place that knows how to stop a cycle, and it
// differs by client.
//
// Against a real device the protocol action is the only correct one: send
// DetectBallCommand with mode Deactivate.
//
// Against the simulator that command is worse than useless. DetectBallCommand
// encodes activate and deactivate as the same 0x11 0x81 opcode with the mode in
// byte 3 (internal/core/protocol/commands.go:13-15), and the simulator dispatches
// on bytes 0 and 1 alone (internal/core/simulator_mock.go:478), so a deactivate
// write cancels the running cycle and immediately starts a new one. Worse, the
// write is processed asynchronously off the simulator's command channel, so it
// lands after this function returns and re-arms behind our back. Measured: with
// the deactivate write, a shot arrives ~11.5s after Disarm; without it, none does.
// Forcing the device state is deterministic instead, because simulateBallDetection
// re-checks deviceState before every stage and returns once it is no longer
// DeviceStateBallDetection.
//
// FLAGGED FOR THE OWNER: this branch is a workaround for an upstream defect and
// it means Phase 1's Disarm button behaves differently from Phase 2's. The honest
// alternatives are (a) ship Phase 1 without a Disarm button, or (b) request a
// fourth sanctioned exception for internal/core/simulator_mock.go to make the two
// modes distinguishable and send that upstream as a PR. Decide before Phase 1
// lands; do not let this quietly become permanent.
func (e *Engine) stopBallDetection() error {
	if e.simulatorMode {
		if e.sim != nil {
			e.sim.SetDeviceState(core.DeviceStateIdle)
		}
		return nil
	}
	if e.sm.GetConnectionStatus() != core.ConnectionStatusConnected {
		return nil
	}
	return e.lm.DeactivateBallDetection()
}

// ---------------------------------------------------------------------------
// Observers and dispatch
// ---------------------------------------------------------------------------

func (e *Engine) registerObservers() {
	// The error text lands just before the Error status at every call site
	// (internal/core/bluetooth_manager.go:107-108), so stash it and use it as the
	// detail on the status we report.
	e.sm.RegisterLastErrorCallback(func(_, newValue error) {
		s := ""
		if newValue != nil {
			s = newValue.Error()
		}
		e.lastLMError.Store(&s)
	})

	e.sm.RegisterConnectionStatusCallback(func(oldValue, newValue core.ConnectionStatus) {
		detail := ""
		if newValue == core.ConnectionStatusError {
			if p := e.lastLMError.Load(); p != nil {
				detail = *p
			}
		}
		e.emit(lmStatusCode(newValue), detail)

		if newValue == core.ConnectionStatusDisconnected {
			select {
			case e.lmDisconnected <- struct{}{}:
			default:
			}
		}
	})

	e.sm.RegisterIntegrationStatusCallback(func(name string, status core.IntegrationStatus) {
		if name != gsproPluginName {
			return
		}
		e.emit(gsproStatusCode(status.Status), status.Error)
	})

	// UI only. connectapi subscribed first, so GSPro already has this shot.
	e.sm.RegisterLastBallMetricsCallback(func(_, newValue *core.BallMetrics) {
		if newValue == nil {
			return
		}
		e.offerDispatch(dispatchItem{kind: dispatchShot, shot: shotValues{
			ballSpeedMPS:       newValue.BallSpeedMPS,
			launchAngleDeg:     newValue.VerticalAngle,
			horizontalAngleDeg: newValue.HorizontalAngle,
			totalSpinRPM:       int32(newValue.TotalspinRPM),
			spinAxisDeg:        newValue.SpinAxis,
		}})
	})
}

func (e *Engine) emit(code int32, detail string) {
	e.offerDispatch(dispatchItem{kind: dispatchStatus, code: code, detail: detail})
}

func (e *Engine) log(msg string) {
	log.Println("android engine: " + msg)
	e.offerDispatch(dispatchItem{kind: dispatchLog, message: msg})
}

// offerDispatch never blocks. Dropping a UI event is strictly better than parking
// an upstream goroutine that may be holding simulator.Base.ConnectMutex.
func (e *Engine) offerDispatch(it dispatchItem) {
	select {
	case e.dispatchCh <- it:
	default:
		log.Println("android engine: listener queue full, dropped an event")
	}
}

// runDispatch is the only goroutine that ever crosses into the listener. It lives
// for the process lifetime, like the engine itself.
func (e *Engine) runDispatch() {
	for it := range e.dispatchCh {
		l := e.currentListener()
		var err error
		switch it.kind {
		case dispatchStatus:
			err = l.OnStatus(it.code, it.detail)
		case dispatchLog:
			err = l.OnLog(it.message)
		case dispatchShot:
			err = l.OnShot(it.shot.ballSpeedMPS, it.shot.launchAngleDeg,
				it.shot.horizontalAngleDeg, it.shot.totalSpinRPM, it.shot.spinAxisDeg)
		}
		if err != nil {
			log.Printf("android engine: listener returned an error: %v", err)
		}
	}
}

func (e *Engine) currentListener() Listener {
	if p := e.listener.Load(); p != nil {
		return *p
	}
	return noopListener{}
}

func (e *Engine) setEndpoint(host string, port int) {
	e.endpointMu.Lock()
	e.gsproHost, e.gsproPort = host, port
	e.endpointMu.Unlock()
}

func (e *Engine) endpoint() (string, int) {
	e.endpointMu.Lock()
	defer e.endpointMu.Unlock()
	return e.gsproHost, e.gsproPort
}

// lmStatusCode maps a core.ConnectionStatus onto the StatusLM* block.
func lmStatusCode(s core.ConnectionStatus) int32 {
	switch s {
	case core.ConnectionStatusScanning:
		return StatusLMScanning
	case core.ConnectionStatusConnecting:
		return StatusLMConnecting
	case core.ConnectionStatusConnected:
		return StatusLMConnected
	case core.ConnectionStatusError:
		return StatusLMError
	default:
		return StatusLMDisconnected
	}
}

// gsproStatusCode maps core.IntegrationStatus.Status onto the StatusGSPro* block.
// The strings come from statusString (internal/core/plugin_host.go).
func gsproStatusCode(s string) int32 {
	switch s {
	case "connecting":
		return StatusGSProConnecting
	case "connected":
		return StatusGSProConnected
	case "error":
		return StatusGSProError
	default:
		return StatusGSProDisconnected
	}
}

// noopListener stands in when no listener is installed, so no call site needs a
// nil guard.
type noopListener struct{}

func (noopListener) OnStatus(code int32, detail string) error { return nil }

func (noopListener) OnShot(a, b, c float64, d int32, f float64) error { return nil }

func (noopListener) OnLog(message string) error { return nil }

// safeListener stops a panic raised inside the listener from killing the dispatch
// goroutine. It is a backstop only: the error returns on Listener are what make a
// thrown Kotlin exception recoverable at all.
type safeListener struct{ inner Listener }

func (s safeListener) OnStatus(code int32, detail string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("listener panicked in OnStatus: %v", r)
		}
	}()
	return s.inner.OnStatus(code, detail)
}

func (s safeListener) OnShot(ballSpeedMPS, launchAngleDeg, horizontalAngleDeg float64, totalSpinRPM int32, spinAxisDeg float64) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("listener panicked in OnShot: %v", r)
		}
	}()
	return s.inner.OnShot(ballSpeedMPS, launchAngleDeg, horizontalAngleDeg, totalSpinRPM, spinAxisDeg)
}

func (s safeListener) OnLog(message string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("listener panicked in OnLog: %v", r)
		}
	}()
	return s.inner.OnLog(message)
}
