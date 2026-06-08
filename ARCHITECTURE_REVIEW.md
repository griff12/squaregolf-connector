# Architecture Review: SquareGolf Connector

## 1. Executive Summary

The codebase works and shows real engineering care in spots (a generic typed event hub, a proper reconnect state machine for sims, a clean pure-parser test suite), but its architecture directly contradicts the owner's two stated goals. **Separation of concerns is not muddy by accident — it is muddy by construction:** nearly everything lives in one flat `package core`, with `launch_monitor.go` (1265 LOC) acting as a god-object that fuses BLE transport, wire-format demux, a command queue, session lifecycle, user workflows, and Omni-vs-Home device branching into one struct. **Open/Closed is currently false:** adding a third launch-monitor model, a non-BLE transport, a third sim, or a second camera vendor each requires surgery across 4-6 core files (`launch_monitor.go`, `state_manager.go`, `constants.go`, `web/server.go`, `main.go`, `config.go`), not an additive new file. The two pain points share a single root cause — there are no enforced layer boundaries and no polymorphic seams where variation happens — so the same refactor that fixes SoC also unlocks the roadmap. There are also several genuine concurrency hazards (an unsynchronized `bluetoothClient` field read across 4 goroutines, a `sync.Once` reset from another goroutine, send-on-closed-channel papered over with `recover()`) and a critical resilience gap (unsolicited BLE disconnects are never detected) that should be fixed regardless of the larger refactor.

## 2. The Core Problem

**One flat `package core` containing two god-objects and a junk-drawer constants file, with device/transport/integration variation expressed as inline `if`-branches instead of interfaces.**

Three concrete manifestations, all verified in the code:

- **God-object #1 — `LaunchMonitor`** (`launch_monitor.go`). It holds `bluetoothClient BluetoothClient` directly (line 48), writes raw BLE with a hardcoded UUID (`writeCommand`, line 614, `WriteCharacteristic(CommandCharUUID, ...)`), runs the 32-deep 150ms-paced command queue (`ensureCommandQueue`/`drainCommandQueue`/`writeCommand`, lines 579-615), demuxes the wire format by hex prefix (`NotificationHandler`, lines 71-139), owns session lifecycle (heartbeat 770-842, charge polling 1026-1063, disconnect teardown 1079), AND embeds user workflows (`ActivateBallDetection` 670, `StartAlignment` 1108 with inline `time.Sleep`). Device variance is ~12 inline `GetDeviceType()==DeviceTypeOmni` branches (181, 209, 229, 257, 303, 322…) plus ~14 Omni-only methods (`sendOmniInitSequence`, `handleOmniStatusRecovery`, `syncOmni*`×4, `startOmniClubMetricsRequest`…). Five responsibilities, zero interfaces.

- **God-object #2 — `StateManager`** (`state_manager.go`, 1086 LOC). One flat 38-field `AppState` struct that every subsystem reads and writes through a global singleton. BLE fields, parsed telemetry, sim-integration status, Omni vendor fields, and camera config all sit at the same level with no namespacing. ~95% of the file is mechanical Get/Set/Register triples copy-pasted per field — and the copy-paste already drifted into a latent bug (`SetClubName` at 363-367 silently has no callback fan-out, unlike every other setter). This struct **is** the "muddy separation" — it's the hub every layer converges on.

- **No boundaries to enforce any of it.** `parse_notifications.go` and `commands.go` are genuinely pure (stdlib-only), but they share a package with the god-objects, so nothing stops future coupling, and the parsed `BallMetrics`/`ClubMetrics` structs carry `json` tags and double as the web API contract (`server.go:60-61`), the StateManager value, AND the parse output — three roles, so a parse-field change ripples into the SPA's JSON. `constants.go` is the symptom in one file: it co-locates LM domain types (`ClubType`, `DeviceType`), BLE UUIDs (`CommandCharUUID`), integration status enums (`GSProConnectionStatus`), sim mock enums, and UI screen names (`ScreenGSPro`, `WindowTitle`).

The downstream effect on Open/Closed: because variation lives as branches inside shared files rather than behind interfaces, **every roadmap item is multi-file surgery.** A third device touches the 12 branch sites + the parser fork + the command encoders + `state_manager.go` + `server.go`. A third sim copy-pastes a 4-file package (`gspro` and `infinitetees` are near-identical today) + adds an enum + two setters + two callbacks + a status struct + handlers. A second camera vendor rewrites `Manager`'s method bodies (SwingCam endpoints are hardcoded in `Arm`/`ShotDetected`/`Cancel`/`UpdateMetadata`).

## 3. Target Architecture

The goal is a small set of packages with dependencies pointing **inward** toward a pure domain, and three explicit seams where the roadmap demands variation: **transport** (BLE vs future TCP/USB), **device profile** (Home vs Omni vs future model), and **integration** (GSPro/InfiniteTees/Camera).

### Package layout

```
internal/
  protocol/        PURE: parse bytes -> typed events; encode commands -> bytes.
                   No imports of core/state/transport. (today's parse_notifications.go + commands.go)
  transport/       DeviceTransport port + ble/ subpackage (the TinyGo adapter, UUID consts live HERE)
  device/          DeviceProfile interface + home/ and omni/ implementations
  monitor/         thin LaunchMonitor coordinator: wires transport+profile+protocol, owns session lifecycle
  state/           StateManager built from generic Field[T] cells
  integration/     Integration interface + registry; gspro/, infinitetees/, camera/ implementations
  app/             composition root: builds the whole graph once, injects everything
  web/             HTTP/WS transport only; depends on app facade + DTOs, never on gspro/infinitetees
  config/, logging/, ui/   unchanged in spirit
```

### The BLE/transport port (replaces the GATT-shaped `BluetoothClient`)

Today `BluetoothClient` (`bluetooth_client.go:9-22`) is 12 methods all phrased in BLE GATT vocabulary (`WriteCharacteristic(uuid, ...)`, `StartScan(prefix)`), so `LaunchMonitor` names UUIDs directly. Replace it with an intent-shaped port; the UUID-addressed GATT client becomes an unexported detail of the `ble` adapter.

```go
// internal/transport
type NotificationKind int
const (
    NotificationDeviceData NotificationKind = iota
    NotificationBatteryLevel
)

type DeviceTransport interface {
    Scan(ctx context.Context, filter DeviceFilter) ([]Device, error)
    Connect(ctx context.Context, d Device) error
    Disconnect() error
    IsConnected() bool
    SendCommand(data []byte) error
    OnNotification(func(kind NotificationKind, data []byte))
    OnConnectionLost(func())          // fixes ERR-1: native disconnect surfacing
    ReadBattery() (int, error)
    ReadSerial() (string, error)
    ReadFirmware() (string, error)
    SetPhaseCallback(func(ConnectionPhase)) // on the interface, no concrete type-assert
    Close() error                     // adapter owns its own lifecycle; no global releaseAdapter
}
```

`monitor.LaunchMonitor` and `BluetoothManager` depend only on this. A future TCP launch monitor implements `DeviceTransport` without impersonating fake characteristics. The `CommandCharUUID`/`BatteryLevelCharUUID` constants move into `transport/ble`, deleting the UUID leak from `monitor` and the `uuid == BatteryLevelCharUUID` branch at `launch_monitor.go:80`.

### The parsing boundary (bytes -> typed events)

Today parsers take `[]string` hex tokens and the demux is stranded in `NotificationHandler`. Make parsing a pure `[]byte -> Message` decoder, and make the metric types domain-only (no `json` tags, no `RawData`, validity already resolved).

```go
// internal/protocol
type Message interface{ isMessage() }

type SensorMessage     struct{ /* ... */ }
type ShotBallMetrics   struct{ BallSpeed, LaunchAngle, /* ... */ float64; ShotID uint64 }
type ShotClubMetrics   struct{ /* ... */ }
type StatusMessage     struct{ /* ... */ }
type BatteryMessage    struct{ Level int }

func Decode(kind transport.NotificationKind, data []byte, profile device.Decoder) (Message, error)

// commands become typed, returning bytes (no hex round-trip):
type Command interface{ Encode(seq uint8, dev DeviceType) []byte }
type Heartbeat   struct{}
type DetectBall  struct{ Mode DetectBallMode; Spin SpinMode }
type SelectClub  struct{ Club ClubType; Hand HandednessType } // Encode handles Home/Omni internally
```

`monitor` switches on the concrete message type and applies state mutations. The fold of `ApplyOmniBallValidityBitmask` into parse means the `validityBitmask` artifact never escapes. Replace the `RawData`-join shot-dedup (`launch_monitor.go:191-204`) with the explicit `ShotID` carried on the event.

### The device profile seam (Home vs Omni vs future)

This is the single most important interface for the owner's device roadmap. It absorbs **every** current fork point: the ~12 inline branches, the Omni-only methods, the parser fork, and the init/sync sequences.

```go
// internal/device
type DeviceProfile interface {
    Match(mfgDataHex string) bool                 // replaces hardcoded DetectDeviceType
    ParseClubMetrics(data []byte) (protocol.ShotClubMetrics, error)
    ParseBallValidity(m *protocol.ShotBallMetrics) // Omni bitmask; Home no-op
    InitSequence() []protocol.Command             // Omni: units/carry/greenspeed/handed; Home: nil
    SettingSyncHandlers() []SettingSync           // absorbs syncOmni* ×4
    AlignmentSequence() []TimedCommand            // lifts StartAlignment's inline time.Sleep into data
}
```

A registry resolves the profile once at connect:

```go
var profiles = []DeviceProfile{ home.Profile{}, omni.Profile{} }
func Resolve(mfgDataHex string) DeviceProfile { /* first p.Match(...) */ }
```

A third model = a new `device/foo/` package + one slice entry. Zero edits to `monitor`.

### The Integration interface + registry (the open/closed payoff)

GSPro and InfiniteTees are near-duplicate copies differing only by status enum, accepted message aliases, and ball-detection trigger timing. Collapse them into one config-driven dialect, and unify all integrations behind one interface the registry ranges over.

```go
// internal/integration
type ConnectionStatus int // the ONE status type; deletes GSProConnectionStatus + InfiniteTeesConnectionStatus

type Integration interface {
    Name() string
    Start(ctx context.Context)
    Stop()
    Connect(host string, port int) error
    Disconnect() error
    Status() ConnectionStatus
    LastError() error
}
```

GSPro and InfiniteTees become two config literals over a shared `gsprodialect` implementation:

```go
gspro := gsprodialect.New(gsprodialect.Config{
    Name: "gspro", DefaultPort: 921,
    ReadyMessages:  []string{"GSPro ready"},
    ActivateBallDetectionOnConnect: false, // GSPro activates on ready
}, deps)

it := gsprodialect.New(gsprodialect.Config{
    Name: "infinitetees", DefaultPort: 922,
    ReadyMessages:  []string{"IT ready", "GSPro ready"},
    ActivateBallDetectionOnConnect: true,
}, deps)
```

`web.Server` holds `integrations map[string]Integration` and serves **one** generic route set `/api/integrations/{name}/{status,connect,disconnect,config}` instead of the 8 hand-duplicated `handleGSProConnect`/`handleInfiniteTeesConnect` handlers (`server.go:643/710`). StateManager stores status in a map keyed by name, not per-integration enum fields.

> **A note on Camera:** do **not** force `camera.Manager` into this `Integration` interface — it has a fundamentally different lifecycle (HTTP fire-and-forget + state listeners, no `Start/Stop` socket loop). Give it its own narrow seam.

### Open/Closed demonstrated — "to add a new camera vendor you now only do X"

Extract a `CameraVendor` interface so `Manager` becomes vendor-agnostic:

```go
// internal/integration/camera
type Vendor interface {
    Arm() error
    ShotDetected(b protocol.ShotBallMetrics) (shotID string, err error)
    Cancel() error
    UpdateMetadata(shotID string, c protocol.ShotClubMetrics) error
}
```

**To add a Foo camera vendor, you now only:** (1) create `camera/foo.go` implementing `Vendor` with Foo's endpoints/DTOs, injecting an `httpDoer` for testability; (2) add one line selecting it in the composition root. No edits to `Manager`'s lifecycle, no edits to `listeners.go` (already vendor-neutral), no edits to `web/server.go`. That is the additive property the owner asked for, and it is the same shape for a third sim (`gsprodialect` config literal) and a third device (`device/foo/` + registry entry).

### Dependency direction (arrows point inward to the pure domain)

```
        main.go ─────────────► internal/app  (composition root: builds graph once)
                                   │
        ┌──────────────┬──────────┼───────────────┬───────────────┐
        ▼              ▼          ▼               ▼               ▼
   internal/web   transport/  integration/    monitor/         state/
   (HTTP/WS)        ble         gspro            │               ▲
        │            │          infinitetees     │               │
        │            │          camera           │               │
        ▼            ▼            ▼               ▼               │
  ──────────────────────────────────────────────────────────────┘
                          internal/protocol   ◄── PURE CORE
                    (bytes ⇄ events, command encode)   no internal imports
                          internal/device (profiles depend only on protocol)

  Rule: protocol imports nothing internal. device imports protocol.
        transport/monitor/state/integration import protocol+device.
        web imports app facade + DTOs only — never gspro/infinitetees directly.
        Nothing imports web. Nothing imports app except main.
```

## 4. Prioritized Findings

| # | Sev | Principle | Location | One-line fix |
|---|-----|-----------|----------|--------------|
| 1 | critical | SRP/OCP | `launch_monitor.go`, `commands.go` | Split god-object into CommandTransport, NotificationRouter, Session, and a `DeviceProfile` interface; LM becomes a thin coordinator |
| 2 | high | DIP | `bluetooth_client.go`, `launch_monitor.go` | Replace GATT-shaped `BluetoothClient` with intent-shaped `DeviceTransport`; UUIDs become a BLE-adapter detail |
| 3 | high | leaky abstraction | `launch_monitor.go`, `bluetooth_manager.go` | Change `notificationHandler` to `func(NotificationKind,[]byte)`; extract pure `Decode([]byte)(Message,error)` |
| 4 | high | leaky abstraction | `parse_notifications.go`, `web/server.go`, `camera/integration.go` | Strip `json`/`RawData`/`validityBitmask` from domain metrics; define web DTOs; carry explicit `ShotID` |
| 5 | high | DIP | `web/server.go`, `config.go` | Introduce an app facade (`GetDeviceSnapshot`, `ListIntegrations`…); web stops importing gspro/infinitetees and hand-mapping enums |
| 6 | high | OCP | `web/server.go`, `main.go`, `simulator/protocol.go`, `camera/integration.go` | Store sims as a slice; collapse 8 handlers into generic `/api/sim/{name}/...`; keep camera separate |
| 7 | high | DRY/OCP | `gspro/*`, `infinitetees/*` | Collapse both into one config-driven `gsprodialect` package; GSPro/IT become config literals |
| 8 | high | polymorphism | `launch_monitor.go`, `constants.go`, `parse_notifications.go` | Introduce `DeviceProfile` interface + mfg-data registry; move ~12 branches + Omni methods onto Omni profile |
| 9 | high | OCP | `state_manager.go`, `constants.go`, `config.go`, `web/server.go` | Replace per-integration status enums with map-keyed `SetIntegrationStatus(id, status)` + `[]IntegrationConfig` |
| 10 | high | data race | `launch_monitor.go`, `bluetooth_manager.go` | Store client in `atomic.Pointer[...]`; route all reads through `GetClient()`; add `-race` connect/notify test |
| 11 | high | data race | `launch_monitor.go` | Replace `cmdQueueOnce = sync.Once{}` reset with a `cmdQueueMu`-guarded per-session struct |
| 12 | high | send-on-closed | `web/server.go` | Make client channel single-writer-owned; close a `done` chan instead; delete the `recover()` crutch |
| 13 | high | go-idiom (interfaces) | `simulator/protocol.go`, `camera/integration.go`, `web/server.go` | Drop `GetStateManager`/`GetLaunchMonitor` from `Protocol`; define small `SimIntegration`; don't force camera in |
| 14 | high | resilience | `tinygo_bluetooth_client.go`, `bluetooth_manager.go` | Register `adapter.SetConnectHandler`; on loss fire a callback that sets `ConnectionStatusDisconnected` |
| 15 | high | panic safety | `camera/listeners.go`, `launch_monitor.go`, `tinygo_bluetooth_client.go` | Add `safeGo` + per-callback `recover()` inside StateManager fan-out loops |
| 16 | high | resilience/DRY | `simulator/connection.go`, `camera/integration.go`, `bluetooth_manager.go` | Extract `resilience.Reconnector` state machine; drive BLE auto-reconnect from the link-loss callback |
| 17 | high | testability | `camera/integration.go`, `camera/listeners.go` | Add non-singleton constructor injecting an `httpDoer`; extract pure buffering decision from goroutine+HTTP |
| 18 | medium | leaky abstraction | `parse_notifications.go`, `launch_monitor.go` | Make parsers take `[]byte`; index bytes directly; convert all `bytesList` handlers together |
| 19 | medium | package boundary | `parse_notifications.go`, `constants.go` | Extract `internal/core/protocol` for parsers + metric structs + protocol enums |
| 20 | medium | DIP | `simulator/protocol.go`, `gspro/integration.go` | Delete the two core-returning methods from `Protocol` (zero callers); sever inward `import core` |
| 21 | medium | DIP | `bluetooth_manager.go`, `tinygo_bluetooth_client.go` | Move adapter lifecycle into the client; inject a client factory; put phase callback on the interface |
| 22 | medium | OCP | `camera/integration.go`, `camera/models.go` | Extract `CameraVendor` interface; move SwingCam endpoints/DTOs into a `SwingCamVendor` impl |
| 23 | medium | composition root | `main.go`, `web/server.go`, integrations | Introduce `internal/app` owning the graph; replace `GetInstance` singletons with `NewX` constructors |
| 24 | medium | reentrancy | `state_manager.go`, `launch_monitor.go` | Per-callback `recover()` now; longer-term move imperative orchestration out of observers |
| 25 | medium | interface segregation | `bluetooth_client.go`, `launch_monitor.go` | Declare a 3-method `deviceIO` consumer interface in `monitor`; LM holds that, not the fat port |
| 26 | medium | DIP | `bluetooth_manager.go`, `tinygo_bluetooth_client.go` | Remove `*TinyGoBluetoothClient` type-assert and `globalAdapter`/`releaseAdapter` references |
| 27 | medium | package layout | core god-files | Extract pure `protocol` package; break parser==DTO coupling; move UI constants to `ui` |
| 28 | medium | constructor ergonomics | `web/server.go`, integrations, `launch_monitor.go` | `NewServer(deps, opts...)` functional options; real constructors instead of arg-ignoring `GetInstance` |
| 29 | medium | generics | `state_manager.go` | Introduce generic `Field[T]{ v, subs }`; collapse the per-field Get/Set/Register triples |
| 30 | medium | type safety | `commands.go`, `launch_monitor.go` | Model commands as typed `Command.Encode(seq, dev)[]byte`; drop hex round-trip |
| 31 | medium | error-handling | `camera/integration.go`, `state_manager.go` | Add `CameraStatus`/`CameraError` state; record HTTP failures; drop the always-nil `error` returns |
| 32 | medium | typed errors | `bluetooth_manager.go`, `launch_monitor.go`, `simulator/connection.go` | Define `ErrNotConnected`/`ErrCommandTimeout`/`ErrQueueFull`; fix dropped `%w` wrapping |
| 33 | medium | testability | `launch_monitor.go`, `launch_monitor_test.go` | Inject a `Clock` seam; route heartbeat/charge/Omni-retry/alignment delays through it |
| 34 | medium | testability | `bluetooth_client.go`, `bluetooth_manager.go` | Promote `SetPhaseChangeCallback` onto the interface; add `bluetooth_manager_test.go` |
| 35 | medium | testability | `gspro/*`, `simulator/connection.go` | Table-test pure conversions now (zero-value `&Integration{}`); inject a dialer into `Base` |
| 36 | medium | testability | `launch_monitor_test.go`, singletons | Make `NewLaunchMonitor`/`NewStateManager` real factories; build isolated graph per test; enable `t.Parallel()` |
| 37 | medium | testability | `launch_monitor.go`, `parse_notifications.go` | Extract pure `classifyNotification(uuid,data,deviceType)`; table-test routing from raw bytes |
| 38 | low | DRY | `simulator_mock.go`, `parse_notifications.go` | (With DeviceProfile work) drive sim emitter + parser off one field-table codec |
| 39 | low | interface segregation | `simulator/protocol.go`, integrations | Delete unused core-returning methods from `Protocol` (same as #20) |
| 40 | low | event bus | `state_manager.go`, integration listeners | Generic `Observable[T]` with `Subscribe(fn) (unsub func())`; fixes camera toggle double-register |
| 41 | low | goroutine lifecycle | `web/server.go` | Demote hot-path log to debug; count dropped events; give `Stop()` a ctx to drain `handleMessages` |
| 42 | low | timer lifecycle | `launch_monitor.go` | Bump `omniClubRetryGen` under its mutex inside `HandleBluetoothDisconnect` (one line) |
| 43 | low | lifecycle/leak | `state_manager.go`, integration listeners | Fold Unregister into the `Observable[T].Subscribe`-returns-unsub redesign (#40) |

## 5. Phased Refactor Plan

Each phase is independently shippable. **Seams (interfaces) land before file-splits** so the big moves are mechanical, not risky. Phases 1-2 are pure safety/correctness with no architecture change — do them first to stop active bugs.

**Phase 0 — Safety net (no architecture change, do immediately).**
- Fix the live races: `atomic.Pointer` for the BLE client (#10); replace the `sync.Once{}` reset with a mutex-guarded session (#11); single-writer WS channel, delete the `recover()` crutch (#12). Add `go test -race` connect/disconnect/notify tests to CI.
- Add `safeGo` + per-callback `recover()` in StateManager fan-out (#15, #24) so one bad packet/observer can't crash the process.
- One-liners: bump `omniClubRetryGen` on disconnect (#42); delete the two unused core-returning methods from `simulator.Protocol`, severing the leaf→core import (#20/#39).
- **Promote existing tests:** `launch_monitor_test.go` already proves `MockBluetoothClient` + `GetWriteHistory` is the real seam — keep it; add `bluetooth_manager_test.go` covering `ReadFirmwareVersion` JSON parse + `ReadSerialNumber` retry via the mock's existing `readReturnData`/`readError` (#34, no interface change needed).

**Phase 1 — Resilience (unblocks reliable operation; no big refactor).**
- Register `adapter.SetConnectHandler` to surface unsolicited BLE disconnects (#14) — the single worst real-world gap. It reuses the existing `RegisterConnectionStatusCallback`→`HandleBluetoothDisconnect` wiring, so no new teardown path.
- Add `CameraStatus`/`CameraError` state and drop the always-nil camera `error` returns (#31).
- Extract the `resilience.Reconnector` state machine from `simulator/connection.go` and feed BLE auto-reconnect from the new link-loss callback (#16).

**Phase 2 — Land the pure layer + the parsing boundary (the highest-payoff SoC move).**
- Extract `internal/core/protocol`: move `parse_notifications.go` + `commands.go` + protocol-only enums (#19, #27). Stdlib-only, compiler-enforced. This is a mechanical repo-wide type rename (`core.BallMetrics` → `protocol.BallMetrics` in ~4 packages) — do it in its own commit.
- Extract the pure `classifyNotification`/`Decode([]byte) Message` decoder out of `NotificationHandler` (#3, #37), and split domain metrics from wire DTOs (#4): drop `json`/`RawData`, fold `ApplyOmniBallValidityBitmask` into parse, carry explicit `ShotID`. Define web DTOs in `internal/web`.
- Move BLE UUID constants into a `transport/ble` package; move UI constants (`ScreenGSPro`, `WindowTitle`) into `ui` (#27).

**Phase 3 — Transport + device-profile seams (unblocks new devices and non-BLE transports).**
- Introduce `DeviceTransport` (intent-shaped) and the narrow `deviceIO` consumer interface; route both `LaunchMonitor` and `BluetoothManager` through it; make `TinyGoBluetoothClient` own its adapter lifecycle and put the phase callback on the interface (#2, #21, #25, #26). **Promote `MockBluetoothClient`/`SimulatorBluetoothClient`** to implement the new port + phase callback, making them first-class test doubles for connection orchestration.
- Introduce `DeviceProfile` + mfg-data registry; move the ~12 Omni branches and the `syncOmni*`/`sendOmniInitSequence`/parser fork onto `omni.Profile` (#1, #8). After this, the LaunchMonitor is a thin coordinator. Inject a `Clock` here so heartbeat/charge/alignment timing is testable and alignment delays become data, not `time.Sleep` (#33).

**Phase 4 — Composition root + integration registry (UNBLOCKS THE CAMERA & SIM ROADMAP).**
- Build `internal/app` as the one owner of the graph; replace every `GetInstance`/`sync.Once` with real `NewX` constructors and inject dependencies (#23, #28, #36). `NewServer` takes already-built integrations + functional options.
- Collapse `gspro`+`infinitetees` into `gsprodialect` config literals (#7); unify behind the `Integration` interface + `map[string]Integration` registry; map-keyed integration status in StateManager; generic `/api/integrations/{name}/...` routes (#5, #6, #9, #13). Web stops importing gspro/infinitetees.
- **This is the phase that makes "add a third sim = one config literal" and (with Phase 5) "add a camera vendor = one file" true.**

**Phase 5 — Camera vendor seam + StateManager generics (the last open/closed gap + boilerplate kill).**
- Extract `CameraVendor`; make `Manager` vendor-agnostic with an injected `httpDoer` (#17, #22). A second camera vendor is now additive.
- Introduce generic `Field[T]`/`Observable[T]` with `Subscribe`-returns-unsub; collapse the 38 hand-rolled triples, fix the `SetClubName` drift and the camera toggle double-register, and add the missing Unregister (#29, #40, #43). Verify the web serialization path doesn't depend on a single shared lock before migrating granularity.
- Web lifecycle hygiene: ctx-cancellable `handleMessages`, drop-event counter, demote hot-path log (#41).

## 6. What's Already Good

Credit where due — these are worth preserving through the refactor:

- **The parsing functions are genuinely pure and well-tested.** `parse_notifications.go` imports only stdlib, has no dependency on BLE/state/orchestration, and `parse_notifications_test.go` covers the tricky cases (`-32768` sentinel, spin decomposition) with clean `[]string` fixtures. This is the easiest thing in the repo to lift into its own package — the hard part (purity) is already done.

- **`StateManager` is a real typed event hub, not an ad-hoc one.** `StateCallback[T any]` is already generic, it does proper old/new diffing, and it correctly snapshots the callback slice under lock then fans out **after** `Unlock()` to avoid self-deadlock. The bones of a good observable are here; the fix (#29) is to stop hand-writing the per-field boilerplate, not to redesign the mechanism.

- **`simulator.Base` has a real reconnect engine** — exponential backoff, `MaxBackoff`, `MaxReconnectTime`, `MaxFailedAttempts`, a proper `connectionThread` loop, plus `SetKeepAlive`/`SetReadDeadline` on the TCP side. This is the most resilient code in the repo; the fix (#16) is to **extract and reuse** it for BLE/camera, not rewrite it.

- **`MockBluetoothClient` + `GetWriteHistory` is an effective test seam.** The entire `launch_monitor_test.go` command-byte assertion suite works because the BLE port is mockable. This proves the port abstraction has value — it just needs to be reshaped (intent vs GATT) and the phase callback promoted onto it so the mock becomes first-class for lifecycle tests too.

- **Dependency direction between core and integrations is already correct.** Grep-verified: `launch_monitor.go`/`commands.go` do **not** import gspro/infinitetees/camera; integrations depend on core, not the reverse. The god-object problem is real, but it has not yet inverted the integration dependency — so the registry refactor (#6) is additive cleanup, not untangling a cycle.

- **`BluetoothManager` already wraps its connect/disconnect goroutines in `recover()`** (lines 128-135, 175-180, 461-468). The recovery policy exists; it's just applied inconsistently. The fix (#15) extends a pattern the codebase already endorses rather than introducing a foreign concept.

- **CLI/server/desktop run modes are cleanly separated in `main.go`**, and config persistence (`config.Manager`) is a sensible single JSON file. The composition-root work (#23) consolidates wiring that is conceptually already close — `main.go` does thread dependencies as constructor args; the problem is the constructors ignore them, not that the call site is wrong.