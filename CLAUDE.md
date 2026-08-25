# squaregolf-connector-android

Fork of `brentyates/squaregolf-connector` (Go, MIT) that runs the connector on Android and
feeds GSPro running under Winlator on the same device via **TCP 18921** (not 921 — see
`PHASE0-FINDINGS.md`).

Read `PROJECT-BRIEF.md` for architecture and phasing before planning any change.
Read `PHASE0-FINDINGS.md` before touching the transport layer.

## Commands

```
go build ./...
go test -race ./...
go vet ./...
gofmt -l .                      # must output nothing

gomobile bind -target=android/arm64 -o app/libs/core.aar ./mobile
./gradlew assembleDebug
./gradlew installDebug
adb logcat -s SquareGolf:V GoLog:V
```

Run `go test -race ./...` before every commit. The upstream code has known concurrency hazards;
a race introduced by our transport must not hide in them.

## Fork discipline — the most important rule here

Upstream is actively developed and we want to keep rebasing onto it.

The rule is **file-scoped**, not package-scoped.

- **Never modify** `internal/core/launch_monitor.go`, `internal/core/state_manager.go`,
  `internal/core/simulator_mock.go`, `internal/core/protocol/`, `internal/core/simulator/`,
  `internal/plugin/`, `internal/plugins/`, `internal/resilience/`, `internal/web/`,
  `internal/ui/`, `web/`, `windows/`, `macos/`.
- **All new code** goes in `internal/transport/android/` and `mobile/`.
- Any change to an upstream file beyond the sanctioned exceptions below: stop and ask. Do not
  work around it by copying an upstream file into a new package.
- Upstream fixes we want are obtained by rebasing, not by patching locally.

### Sanctioned exceptions — the only upstream files we may touch

`internal/core` is a single Go package and Go links at package granularity, so
`tinygo_bluetooth_client.go` sitting untagged inside it makes the whole package —
`launch_monitor.go`, `state_manager.go`, both test doubles — impossible to build for
android/arm64. Proven: `go list -deps ./internal/core` pulls `tinygo.org/x/bluetooth`,
`winrt-go` and `go-ole`. Three carried patches, each minimal:

1. `internal/core/tinygo_bluetooth_client.go` — add `//go:build !android`. It is the **only**
   file in the repo importing `tinygo.org/x/bluetooth`.
2. New `internal/core/tinygo_bluetooth_client_android.go` (`//go:build android`) — stubs for
   the symbols `bluetooth_manager.go` references: `NewTinyGoBluetoothClient`,
   `ConnectionPhase`, `PhaseScanning`, `PhaseConnecting`, `globalAdapter`, `releaseAdapter`.
3. `internal/core/bluetooth_manager.go` — **one** edit: widen the
   `*TinyGoBluetoothClient` type assertion in `setupPhaseCallback` to a capability interface
   (`SetPhaseChangeCallback` / `SetConnectionLostCallback`) so a non-TinyGo client still gets
   connection-lost detection. **Send this upstream as a PR** — it is a real bug for them too.
   Link the PR here once open.

The hardcoded `NewTinyGoBluetoothClient()` fallback at `:104` needs **no** change: the android
stub returns an error, `:105` sees `err != nil` and returns at `:118`, so the nil client never
reaches the field. The fallback becomes an honest error path for free.

Landed as `ac0b7df` (the widening, upstream-bound) and `08285f3` (the tag split), kept separate
so the PR can be cherry-picked and rebases stay tractable.

## Architecture boundaries

- `internal/transport/android/` implements upstream's `BluetoothClient` interface by delegating
  to Kotlin. It holds no protocol knowledge — no UUIDs beyond what upstream already defines, no
  parsing, no shot logic.
- `mobile/` is the gomobile FFI shim and nothing else. It flattens Go APIs into boundary-legal
  signatures and translates callbacks. **No business logic.** If a function in `mobile/` is
  doing more than marshalling, it belongs elsewhere.
- Kotlin owns BLE, permissions, service lifecycle, and UI. It does not parse Square Golf packets
  or speak Open Connect — those stay in Go.

## gomobile boundary rules

Exported signatures in `mobile/` may only use: numeric types, `bool`, `string`, `[]byte`,
`error`, and types declared in `mobile/` itself.

Not allowed across the boundary: maps, slices other than `[]byte`, channels, generics,
variadics, embedded structs, `time.Time`.

Kotlin→Go callbacks are Go interfaces implemented on the Kotlin side. Keep them small; every
method is an FFI hop.

## GSPro Open Connect — hard-won rules

These come from IL analysis of GSPconnect and cost real time to discover. Full detail in
`PHASE0-FINDINGS.md`.

- **One JSON object per `write()`.** Never pipeline two into one write.
- **Send a heartbeat first**, before any shot.
- **Hold one long-lived connection; never half-close.** GSPconnect never checks `Read()`'s
  return value and will hot-spin and wedge on a client FIN.
- **Don't trust its "Connected" indicator** — it is never cleared on client disconnect. Carry
  independent liveness.
- **Spin is order-sensitive:** send `{SpinAxis, TotalSpin}` **or** `{BackSpin, SideSpin}`, never
  a half-filled mix.
- **Always send a non-null `ShotDataOptions`, never a top-level `data` object** — that latches
  the connection into legacy mode permanently and all responses go silent.
- Code 200 acks coalesce on a 200 ms timer. Cap at ~5 shots/sec. Codes 202/203 are undocumented
  and must not be treated as fatal.

## Don't

- Don't log ball or club metrics at info level. Debug only — shot spam buries connection
  diagnostics, which are what you actually need when this breaks.
- Don't add a Kotlin BLE library dependency without asking. Platform APIs first; Nordic's
  library is a considered choice, not a default.
- Don't use `time.Sleep` for sequencing in new code. Upstream does this and it's called out as a
  defect in their own architecture review.
- Don't hardcode `127.0.0.1:18921`. Host and port are settings with those defaults.
- Don't write to `internal/state` singletons from the transport layer. Emit through the
  callbacks upstream already defines.

## Testing

- Go core: table tests, no hardware. `SimulatorBluetoothClient` drives synthetic shots end to
  end — use it for anything that isn't specifically BLE.
- The transport adapter is tested through a fake Kotlin-side implementation of the callback
  interface, not through an emulator.
- Every bug gets a failing test before the fix.
- The e2e pipeline test is behind `-tags e2e` because it takes ~34s (the simulator free-runs a
  shot every ~11.5s and cannot be hurried):
  `go test -tags e2e -run TestPipelineSimulatorToGSPro ./internal/transport/android/`

### Known-failing baseline - do not "fix" these

`-race` requires cgo, which requires a C toolchain on PATH. WinLibs MinGW-w64 (UCRT) is
installed at `%LOCALAPPDATA%\Microsoft\WinGet\Packages\BrechtSanders.WinLibs.POSIX.UCRT_*`.
Without it `go build ./...` also fails on `webview_go` in the root package and `internal/ui`.

With cgo available, `go test -race ./...` is clean **except**:

```
--- FAIL: TestSeedIntegrationConfigUsesLegacySettingsWhenGenericConfigIsAbsent
    main_test.go:45: seeded config = map[autoConnect:true host:10.0.0.5 port:921]
```

This is **upstream's, not ours** - verified by running the same test on a pristine `cb49e8d`
worktree, where it fails identically. It reads the developer's real on-disk config rather
than a fixture, so it passes or fails depending on the machine. Worth an upstream issue.

Everything else, including `internal/transport/android`, is race-clean.

**Beware:** `cc` on this machine resolves to `~/.local/bin/cc.ps1`, the Claude Code project
launcher. Go defaults to `CC=gcc` on Windows so cgo is unaffected, but never set `CC=cc`.

## Workflow

Use plan mode for anything touching the transport or FFI boundary. Those two places are where a
wrong assumption costs a day, and they're both hard to see from a single file.

When exploring upstream Go code, delegate to a subagent and take back a summary — the codebase
has a 1200-line god-object in it and reading it inline will eat the context window.
