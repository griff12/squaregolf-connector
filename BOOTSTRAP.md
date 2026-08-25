# Bootstrap — what to paste into Claude Code, in order

Each block is one session. Run `/clear` between them. Don't skip ahead; every gate exists
because failing it means the next phase can't work.

---

## Phase 0 — ✅ DONE (2026-08-25)

**Gate passed.** An Android process sent a hand-built Open Connect V1 shot to GSPro Connect
running under Winlator and got back:

```json
{"Code":200,"Message":"Club & Ball Data received","Player":null}
```

The loopback assumption held — Winlator does not isolate the network namespace. The blocker
was that **921 is a privileged port** and no Android app uid can bind it. GSPconnect's
listener was patched to **18921**.

Full detail, rollback, and protocol gotchas: `PHASE0-FINDINGS.md`. **Read it before Session 2.**

If you ever need to re-verify by hand, from Termux:

```
nc 127.0.0.1 18921
```

...and paste an Open Connect V1 payload. Send a heartbeat first, one JSON object per write,
and keep the connection open — GSPconnect wedges on a half-close.

---

## Session 1 — Set up the repo

> Fork `github.com/brentyates/squaregolf-connector` and set it up as an Android port. I've
> written CLAUDE.md and PROJECT-BRIEF.md — read both first.
>
> In plan mode, explore the upstream repo and report back before changing anything:
> - the exact current shape of the `BluetoothClient` interface, method by method
> - every place it's constructed or injected
> - how `SimulatorBluetoothClient` and `MockBluetoothClient` implement it
> - which packages import the TinyGo bluetooth library
> - what `main.go`'s server mode does and whether it's useful to us
>
> Then propose the minimal set of new files for `internal/transport/android/` and `mobile/`.
> Don't write code yet.

Read the plan properly. This is the session that determines whether the port surface is one
interface or several — everything downstream depends on getting it right.

---

## Session 2 — Go core building for Android

> Implement Phase 1 from PROJECT-BRIEF.md: get the Go core building for android/arm64 and
> bound to an AAR, driven by `SimulatorBluetoothClient`. No real BLE yet.
>
> Work in this order and stop at each step for me to verify:
> 1. `go build` succeeds for android/arm64
> 2. `mobile/` shim exposes: start, stop, connect-to-sim, fire-synthetic-shot, and a status
>    callback interface — all boundary-legal
> 3. `gomobile bind` produces an AAR without errors
> 4. Minimal Kotlin app with one button that fires a synthetic shot

**Gate:** synthetic shot from the Kotlin app appears in GSPro inside Winlator. The pipeline is
proven with no hardware in the loop.

Point the GSPro output at `127.0.0.1:18921`, and follow the connector notes in
`PHASE0-FINDINGS.md` — especially: heartbeat first, one JSON object per write, never
half-close the connection.

---

## Session 3 — Real BLE

> Implement Phase 2: a real `BluetoothClient` backed by Android BLE.
>
> Kotlin side handles scan, connect, GATT service discovery, notification subscribe, and
> characteristic writes, exposed to Go through the callback interface from Session 2. Use the
> UUIDs and command bytes upstream already defines — don't invent protocol.
>
> Start with a failing test for the adapter using a fake Kotlin-side implementation, then
> implement.
>
> Handle up front: runtime permissions for Android 12+, MTU negotiation after connect, and
> link-loss surfacing. Upstream's architecture review flags undetected disconnects as their
> worst real-world gap — don't inherit it.

**Gate:** a real Omni shot reaches GSPro.

---

## Session 4 — Survive a round

> Implement Phase 3: foreground service with `connectedDevice` type so BLE holds while
> Winlator is foregrounded, auto-reconnect on both the BLE and TCP legs, and battery/status in
> the UI.
>
> Reuse upstream's existing reconnect engine rather than writing a new one — their
> `simulator.Base` already has exponential backoff with sensible caps.
>
> On the TCP leg specifically: GSPconnect's own reconnect handling is broken (it never checks
> `Read()`'s return value and hot-spins on a client FIN). Hold one long-lived connection, never
> half-close, and don't trust its "Connected" indicator — carry independent liveness.

---

## Notes on running these

- **Plan mode for Sessions 1 and 3.** Transport and FFI are where a wrong assumption costs a day.
- **`/clear` between sessions.** These are unrelated tasks and context bleed makes later
  sessions worse, not better.
- **One issue per prompt** when debugging. A prompt listing five problems gets worse results
  than five prompts.
- **Point at files, don't describe them.** `@internal/transport/android/ble.go` beats
  explaining what's in it.
- **Add to CLAUDE.md when Claude gets something wrong twice.** Don't write rules in advance for
  mistakes that haven't happened — the file earns its length.
- **Don't launch GSPro via `GSPLauncher.exe`**, and never touch Force Update / Rollback /
  Restore Files — each silently reverts the port patch. Use `connector.bat`.
