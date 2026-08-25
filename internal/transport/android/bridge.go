// Package android adapts upstream's core.BluetoothClient onto an Android BLE
// stack that lives in Kotlin, and owns the composition of upstream's singletons
// into a runnable engine.
//
// The package holds no protocol knowledge: no packet parsing, no shot logic and
// no UUIDs of its own. Characteristic UUIDs arrive as opaque strings from
// upstream (internal/core/constants.go:42-46) and are handed straight through.
// Nothing here writes to upstream state singletons directly; state reaches the
// application only through the callbacks upstream already defines.
//
// No file in this package carries a build tag. Everything compiles on the
// developer host as well as android/arm64, so `go test -race ./...` exercises it
// against a fake Bridge with no emulator.
//
// # Boundary shape
//
// Two directions cross the gomobile FFI, and they are not symmetrical:
//
//	Go -> Kotlin   the Bridge interface below. gobind turns it into a Java
//	               interface that Kotlin implements. Every method must use only
//	               boundary-legal types.
//	Kotlin -> Go   methods on Engine, re-exported through mobile/.
//
// Bridge is declared here, in a package gobind never sees, so this package stays
// free of any gomobile dependency and is testable with a plain Go fake. mobile/
// declares a structurally identical interface (mobile.BleBridge) which IS bound;
// because Go interfaces are structural, the gobind proxy that Kotlin produces
// satisfies Bridge with no adapter. mobile/ carries
//
//	var _ android.Bridge = BleBridge(nil)
//
// as a compile-time guarantee that the two declarations stay in step.
//
// # Two signatures that could not cross, and what replaced them
//
// core.BluetoothClient has exactly two members gomobile cannot express:
//
//	StartNotifications(uuid string, handler func([]byte)) error
//	GetDiscoveredDevices() []string
//
// Neither is forwarded. Both are inverted so the crossing disappears:
//
//   - Notifications. Bridge.StartNotifications carries only the UUID; Kotlin
//     enables the CCCD and nothing else. The func([]byte) never leaves Go: it is
//     held in a registry inside Client and re-joined with its data when Kotlin
//     pushes a packet back through Engine.OnNotification.
//
//   - Discovered devices. Kotlin pushes one device at a time into
//     Engine.OnDeviceDiscovered (three strings, all boundary-legal). Go owns the
//     table, so GetDiscoveredDevices() []string is served entirely from Go
//     memory and never marshals anything. The flattening problem only reappears
//     when mobile/ shows the list to the Kotlin UI, and it is solved there with a
//     count+index accessor pair over a mutex-guarded snapshot -- never a joined
//     string, because a BLE LocalName is attacker-influenced free-form UTF-8 and
//     that name is the identity later passed back to Connect.
//
// # The []byte aliasing rule -- the single most dangerous fact here
//
// Proven from the gomobile source in use
// (golang.org/x/mobile@v0.0.0-20260821190718-4776eadac327):
//
//   - bind/gen.go:35-44 defines modeTransient ("Transient byte slices don't need
//     copying") and modeRetained ("Retained byte slices need an intermediate
//     copy").
//   - bind/genjava.go:1225 passes every parameter of an exported Go function as
//     modeTransient, and bind/genjava.go:1250 emits go_seq_release_byte_array
//     immediately after the call returns.
//   - bind/java/seq_android.go.support:86-98 shows toSlice with cpy==false
//     building the Go slice with unsafe.Pointer directly over the JNI-pinned
//     array.
//
// Therefore a []byte parameter arriving from Kotlin (OnNotification's data)
// ALIASES JNI memory that is released the instant the Go function returns. It
// MUST be copied before it is stored or handed to another goroutine. Retaining
// it is a use-after-free that presents as sporadic garbage in shot packets, not
// as a crash.
//
// The reverse case is safe: bind/genjava.go:1295 marshals an interface method's
// return value as modeRetained, so the []byte that Bridge.ReadCharacteristic
// returns is a copy owned by Go.
//
// # Errors
//
// Kotlin signals failure by THROWING, never by a return code. gobind declares
// every Bridge method that returns error as `throws Exception` on the Java side
// (bind/genjava.go:840-855) and converts a thrown Throwable into a Go error whose
// Error() is Throwable.getMessage() (bind/genjava.go:1286-1288, 1494). A
// Throwable with a null message yields the empty Go string
// (bind/java/seq_android.c.support:181-186), so Client normalises empty messages
// rather than emitting a bare "operation: ".
//
// # Threading contract for the Kotlin implementor
//
//   - Every Bridge method is called from a Go-owned worker thread that JNI has
//     attached to the JVM. It is NEVER the Android main thread, so blocking is
//     allowed and expected. It is also never a binder thread, so a Bridge method
//     may safely block waiting for a BluetoothGattCallback.
//   - Connect, Disconnect, WriteCharacteristic, ReadCharacteristic,
//     StartNotifications and StopNotifications are SYNCHRONOUS: they return only
//     when the underlying GATT operation has completed, or throw. StartScan and
//     StopScan are asynchronous -- they arm/disarm the scanner and return.
//   - Every synchronous method MUST enforce its own timeout and throw on expiry.
//     Android drops GATT callbacks (bonding races, radio resets); a lost callback
//     with no timeout parks a Go goroutine and wedges the launch monitor's
//     command queue.
//   - Client serialises Connect, WriteCharacteristic and ReadCharacteristic so at
//     most one is in flight, matching Android's one-outstanding-GATT-operation
//     rule. Disconnect, StopNotifications and StopScan are deliberately EXEMPT:
//     they are cancel paths and must never queue behind a blocked operation.
//     Kotlin must make them safe to call at any time, and Disconnect must cause
//     any in-flight operation to fail fast.
//   - Callbacks in the other direction (Engine.OnNotification,
//     Engine.OnDeviceDiscovered, Engine.OnConnectionStateChanged) arrive on
//     Android binder threads. They copy, enqueue and return promptly, so a binder
//     thread is never held while Go runs upstream's handler chain. That is not a
//     nicety: a notification handler reaches LaunchMonitor.SendCommand
//     (internal/core/launch_monitor.go:649-670), which blocks up to ten seconds
//     waiting for a BLE write. Running that inline on the binder thread that must
//     later deliver onCharacteristicWrite is a guaranteed deadlock.
package android

// Connection states reported by Kotlin through Engine.OnConnectionStateChanged.
// int32 rather than Go's int so the bound constants surface in Kotlin as Int
// rather than Long -- gobind maps Go int to Java long.
//
// These mirror what BluetoothGattCallback.onConnectionStateChange and the scan
// callbacks already tell Kotlin; nothing here is invented state.
const (
	// ConnStateDisconnected: the link is down. If Client believed it was up,
	// this is an unsolicited disconnect and drives SetConnectionLostCallback.
	ConnStateDisconnected int32 = 0
	// ConnStateScanning: Kotlin is scanning for the connect target. Drives
	// core.PhaseScanning.
	ConnStateScanning int32 = 1
	// ConnStateConnecting: the target was found; a GATT connection is being
	// established. Drives core.PhaseConnecting.
	ConnStateConnecting int32 = 2
	// ConnStateConnected: GATT is connected AND service/characteristic discovery
	// has completed, so writes and reads may be issued.
	ConnStateConnected int32 = 3
)

// Bridge is the Go -> Kotlin half of the transport. Kotlin implements it; every
// call is one FFI hop, which is why it has eight methods and not twelve.
//
// Every signature uses only gomobile-legal types: string, []byte, error. No func
// parameters, no slices other than []byte, no maps, no variadics. gobind SILENTLY
// SKIPS an interface method whose signature it cannot express
// (bind/genjava.go:1255-1258): it emits a comment, not an error. An illegal
// signature added here does not fail the build -- it produces a Java interface
// quietly missing a method. Treat this interface as frozen unless the change is
// deliberate, and keep the "grep skipped" gate in the build runbook.
type Bridge interface {
	// Connect establishes a GATT connection and completes service and
	// characteristic discovery, returning only when the device is ready for
	// reads and writes, or throwing.
	//
	// Target resolution, in order: deviceAddress if non-empty (Kotlin can go
	// straight to BluetoothAdapter.getRemoteDevice with no scan); else the device
	// whose advertised name equals deviceName; else the first device whose
	// advertised name starts with namePrefix.
	//
	// namePrefix is whatever prefix upstream last passed to StartScan, so the
	// transport never invents a device identity of its own. It is "" when no scan
	// has run, in which case Kotlin must rely on deviceAddress or deviceName.
	//
	// Kotlin must report the chosen device through Engine.OnDeviceDiscovered
	// before or with its OnConnectionStateChanged(ConnStateConnected) call.
	// Without that, GetConnectedDeviceManufacturerData returns "" and upstream's
	// DetectDeviceType resolves every device as Home, not Omni.
	Connect(deviceName string, deviceAddress string, namePrefix string) error

	// Disconnect tears down the GATT link and releases the client. It must be
	// safe to call when not connected (a no-op), safe to call concurrently with
	// any other Bridge method, and must cause an in-flight blocking operation to
	// fail fast rather than hang.
	Disconnect() error

	// StartScan arms the scanner. It is asynchronous: it returns as soon as
	// scanning has started. Kotlin reports each match through
	// Engine.OnDeviceDiscovered.
	//
	// namePrefix is a PREFIX, which Android's ScanFilter.setDeviceName cannot
	// express (it matches exactly). Kotlin must filter in its ScanCallback and
	// must NOT report every advertisement it sees; Go re-applies the prefix on
	// ingest as a backstop, but the FFI hop has already been paid by then.
	StartScan(namePrefix string) error

	// StopScan disarms the scanner. Already-reported devices stay in Go's table,
	// matching upstream (internal/core/tinygo_bluetooth_client.go:222).
	StopScan() error

	// WriteCharacteristic writes data to the characteristic with the given UUID
	// and returns when the write has been acknowledged, or throws. Kotlin chooses
	// write-with-response or write-without-response from the characteristic's
	// properties. data is a fresh Java byte[]; Kotlin may retain it.
	WriteCharacteristic(uuid string, data []byte) error

	// ReadCharacteristic reads the characteristic with the given UUID. The
	// returned array is copied into Go memory by gobind, so Kotlin may reuse its
	// buffer afterwards.
	ReadCharacteristic(uuid string) ([]byte, error)

	// StartNotifications enables notifications on the characteristic: set the
	// local notification flag and write the CCCD descriptor, then return. There is
	// no handler parameter -- a Go func cannot cross the boundary. Kotlin delivers
	// every subsequent packet to Engine.OnNotification(uuid, data).
	StartNotifications(uuid string) error

	// StopNotifications disables notifications on the characteristic. The Go-side
	// handler has already been removed by the time this is called, so a packet
	// that races the disable is dropped rather than delivered.
	StopNotifications(uuid string) error
}
