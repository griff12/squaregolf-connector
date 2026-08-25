# SquareGolf Connector — Android Port

Feed Square Golf Omni ball data to GSPro running under Winlator on the same phone, over
the GSPro Open Connect API on **TCP 18921** (see "Transport", below — it is not 921).

## The core decision: port the transport, don't rewrite the app

Upstream `brentyates/squaregolf-connector` (Go, MIT) already contains everything hard about
this problem:

- BLE protocol parse/encode for Square Golf devices, as a pure stdlib-only layer
- Home vs Omni device differences
- A GSPro Open Connect dialect with an exponential-backoff reconnect engine
- `MockBluetoothClient` and `SimulatorBluetoothClient` test doubles

None of that is worth reimplementing in Kotlin. What does **not** port is exactly one thing:
it reaches the radio through the TinyGo bluetooth package, which binds to BlueZ /
CoreBluetooth / Windows. Android uses none of those.

Upstream already isolates that behind a `BluetoothClient` interface. **That interface is the
entire port surface.**

## Architecture

```
┌─────────────────────────────────────────────┐
│ Kotlin app                                  │
│  - BLE scan/connect/GATT (Android APIs)     │
│  - runtime permissions, foreground service  │
│  - UI: device status, battery, last shot    │
└───────────────┬─────────────────────────────┘
                │ gomobile AAR boundary
┌───────────────▼─────────────────────────────┐
│ Go core (fork of squaregolf-connector)      │
│  protocol/     bytes <-> events   UNCHANGED │
│  device/       Home & Omni profiles UNCHANGED│
│  monitor/      session lifecycle  UNCHANGED │
│  integration/  GSPro dialect      UNCHANGED │
│  transport/android/   NEW - the whole port  │
│  mobile/              NEW - FFI shim        │
└───────────────┬─────────────────────────────┘
                │ TCP client
┌───────────────▼─────────────────────────────┐
│ 127.0.0.1:18921  →  GSPro inside Winlator   │
└─────────────────────────────────────────────┘
```

Two new packages. Everything else is upstream and stays rebaseable.

## Transport — RESOLVED, see PHASE0-FINDINGS.md

The load-bearing assumption held: **Winlator does not isolate the network namespace.** Wine's
`127.0.0.1` *is* Android's `127.0.0.1`. Verified by observation — Winlator's sockets appear in
Android's own `/proc/net/tcp` under its app uid.

What blocked it was unrelated: **921 is a privileged port** and no Android app uid can bind it
(`ip_unprivileged_port_start = 1024`). GSPro Connect's listener never came up at all.

Fixed with a 4-byte, length-preserving patch to `GSPconnect.exe` moving its Open Connect
listener from 921 to **18921**. Proven end-to-end:

```json
{"Code":200,"Message":"Club & Ball Data received","Player":null}
```

Full detail, rollback procedure, and the list of actions that silently revert the patch are in
`PHASE0-FINDINGS.md`. Read it before touching the transport layer — it also documents a set of
Open Connect protocol quirks (framing, ack coalescing, spin field pairing, a reconnect flaw)
that will otherwise cost real time in Phase 1.

## gomobile boundary constraints

`gomobile bind` supports a restricted type set across the FFI: basic numeric types, `string`,
`[]byte`, `error`, and structs/interfaces declared in the bound package. No maps, no slices
other than `[]byte`, no channels, no generics.

So `mobile/` is a deliberately narrow shim that flattens the Go API into boundary-legal
signatures. Kotlin implements a Go interface for BLE callbacks; Go calls into it for writes and
scans. Keep the shim thin — logic belongs in the Go core or the Kotlin app, never in the
translation layer.

## Phases

Each ships independently. Do not start the next until the current one is proven.

**Phase 0 — Prove loopback. ✅ DONE (2026-08-25).** Gate passed: a hand-built Open Connect V1
shot from an Android process was acknowledged by GSPro Connect with Code 200. See
`PHASE0-FINDINGS.md`.

**Phase 1 — Go core on Android, no BLE.** Fork, build for `android/arm64`, bind to an AAR,
drive it with `SimulatorBluetoothClient`. Minimal Kotlin UI: one button that fires a synthetic
shot. Gate: a synthetic shot from the Kotlin app appears in GSPro inside Winlator. **This
proves the whole pipeline with no hardware involved.**

**Phase 2 — Real BLE.** Implement `BluetoothClient` in Kotlin against Android BLE. Scan,
connect, subscribe to notifications, write commands. Gate: a real Omni shot reaches GSPro.

**Phase 3 — Make it survive a round.** Foreground service so BLE holds when the screen is off
and Winlator is foregrounded. Link-loss detection and auto-reconnect on both BLE and TCP legs.
Battery and status surfaced in the UI.

**Phase 4 — Polish.** Club selection, handedness, alignment, settings persistence.

Phase 1 before Phase 2 is the point of the whole plan: it separates "does the architecture
work" from "does BLE work," so a failure has one cause instead of two.

## Android specifics that will bite

- **Permissions.** Android 12+ needs `BLUETOOTH_SCAN` and `BLUETOOTH_CONNECT` at runtime.
  Declare `neverForLocation` on the scan permission or you inherit a location prompt you don't
  need.
- **Foreground service.** Required to keep a BLE connection alive while Winlator is in front.
  Needs `connectedDevice` service type and its manifest permission.
- **Battery optimization.** Samsung is aggressive. The app needs an exemption or it gets killed
  mid-round.
- **MTU and connection interval.** Request a larger MTU after connect. Shot data is
  latency-sensitive; the default interval is not tuned for it.
- **One long-lived TCP connection.** GSPconnect never checks `Read()`'s return value; a client
  FIN makes it hot-spin instead of re-accepting, wedging the listener until restart. Never
  half-close. Its own UI reports "Connected" after the client is gone, so carry independent
  liveness.

## Reference

- Upstream: `github.com/brentyates/squaregolf-connector` (MIT)
- Upstream's own `ARCHITECTURE_REVIEW.md` documents the intended `DeviceTransport` seam — read
  it before touching the transport layer
- GSPro Open Connect: connector is the client, GSPro is the server. Protocol per
  `gsprogolf.com/GSProConnectV1.html`; **port 18921 on this device**, not the documented 921.
