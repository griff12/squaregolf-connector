# Phase 0 — SOLVED

**Date:** 2026-08-25
**Device:** Galaxy Z Fold 8 (SM-F971U1), Android 17, stock, **not rooted**
**Winlator:** brunodev85 official v11.2 (uid 10472, debuggable)
**GSPro:** public-beta-v.3.49.9.1 / connect v3.2.50, at `/sdcard/Download/GSProV1/`

---

## Verdict: the architecture in PROJECT-BRIEF.md works. Gate passed.

Proven end-to-end on hardware:

```json
{"Code":200,"Message":"Club & Ball Data received","Player":null}
```

An Android process connected to `127.0.0.1:18921`, sent an Open Connect V1 heartbeat
followed by a shot with ball *and* club data, and received GSPro Connect's Code 200
acknowledgement. No root.

---

## The two questions, answered

### 1. Is Wine's loopback Android's loopback? YES.

Not inferred — **observed**. Winlator's sockets appear in Android's own `/proc/net/tcp`
under uid 10472, and a connection from an Android process shows up as a real kernel socket
pair on container loopback:

```
0100007F:2382  00000000:0000   0A  uid=10472      LISTEN 127.0.0.1:9090
0100007F:2382  0100007F:CFF2   08  rx_queue=0xC4  196 bytes delivered
```

Winlator's launcher is a plain `ProcessBuilder` fork/exec of `box64` as a child of the
Android app process — no proot in that path, no `unshare`, no usermode stack. And a network
namespace needs `CAP_SYS_ADMIN`, which an unrooted app cannot have.

**The Go relay fallback in the original plan was never needed and would not have helped.**

### 2. So why did nothing work? The privileged port.

`/proc/sys/net/ipv4/ip_unprivileged_port_start = 1024`, and app uids have no
`CAP_NET_BIND_SERVICE`. Measured boundary: 921 and 1023 → `bind: Permission denied`;
1024 and 18921 → OK.

GSPconnect's own log had been saying so all along, 2,478 times, retrying every 200 ms:

```
SocketException (0x80004005): Access denied.
  at System.Net.Sockets.Socket.Bind
  at System.Net.Sockets.TcpListener.Start
  at VGPconnect.OpenSDKv1.ListenForIncommingRequests ()
```

`OpenAPIUseAltPort=true` only moves 921 → 922. Also privileged. Useless.

---

## The fix: one 4-byte patch

`listenPort` is a single field with exactly five writers and one reader, feeding
`new TcpListener(listenPort)`. Two writers set 921; both patched to **18921**.

| Site | Offset | Before | After |
|---|---|---|---|
| `OpenSDKv1::.ctor` field initializer | 608276 | `20 99 03 00 00` | `20 E9 49 00 00` |
| `SimpleConForm_Load`, `LMtype==0` (**governing**) | 612453 | `20 99 03 00 00` | `20 E9 49 00 00` |

`ldc.i4` is fixed 5 bytes, so the edit is length-preserving — no IL displacement, no method
size, metadata, RVA or section changes. Anchor `7D 11 0F 00 04` (`stfld listenPort`,
token `0x04000F11`) verified immediately after both sites.

```
original  md5 5e61a605e4d92b8b05e2ed1acb162713   (backed up as GSPconnect.exe.orig921)
patched   md5 a7d3f71d532e0807970f2764e55d5829
size      7,001,792 — unchanged
```

**Why 18921:** unprivileged; *below* the ephemeral range (`ip_local_port_range = 32768 60999`)
so no outbound socket can transiently steal it; unassigned by IANA; bind-tested free twice
on-device. Deliberately not 12495 — that's the Nova brand port and would be ambiguous.

### Rollback

```
adb -s 10.0.0.172:37581 shell "cp /sdcard/Download/GSProV1/Core/GSPC/GSPconnect.exe.orig921 \
                                  /sdcard/Download/GSProV1/Core/GSPC/GSPconnect.exe"
```

### What silently reverts the patch — avoid these

- Launching via **`GSPLauncher.exe`** when GSPro's cloud reports a newer Connect version:
  it extracts over `Core\GSPC` with `overwrite:true`
- The launcher's **Force Update** checkbox, or GSPconnect's **Rollback** button
- Running **`GSProConnectRestoreFiles.exe`** / "Restore Files"

Launch with `connector.bat` (direct exec) and none of these fire. ClickOnce never validates
on that path — proven by the fact that the *shipped* exe already mismatches its own manifest
hash (7,001,792 B vs the manifest's 6,991,360 B) and runs fine.

### Selecting the mode

`licenseType` in `user.config` drives auto-restore. It must be `APIv1`:

```
/data/data/com.winlator/files/rootfs/home/xuser-1/.wine/drive_c/users/xuser/
  AppData/Local/VGPconnect/GSPconnect.exe_Url_zcnio3o0ue4trd14bgzozfqk5ibm3u4x/1.0.0.0/user.config
```

Readable and writable via `adb shell run-as com.winlator` — **Winlator is a debuggable
build, so the whole Wine prefix is reachable without root.** Useful well beyond this fix.

Confirm the right path is live by the log line `INFO VGPconnect.OpenSDKv1 - Open Connect
Started` (not `OpenConForm`).

---

## Rejected alternatives, and why

| Option | Why not |
|---|---|
| Root + `sysctl ip_unprivileged_port_start=0` | Works, but costs Knox permanently. User declined. |
| Licence conversion → ProTee VX (12321) / Nova (12495) / VTrack (12485) | Genuinely viable and vendor-sanctioned: same class, same listener, byte-identical protocol; `ValidClientSoftwareFound` is a 2-byte stub (`ldc.i4.1; ret`). Rejected because conversion is a *swap* — it would hide the OpenAPI button — and it POSTs `{LicenseType, UID:<licence key>}` to a vendor endpoint declaring hardware not owned. |
| Open Control (Legacy), 9090 | Works today with zero changes and **was used to prove the loopback path first**. Rejected for production: `MinShotData` only — ball data, no club data. |
| Unprivileged user namespace | Impossible: kernel ships `# CONFIG_USER_NS is not set`. |
| `LD_PRELOAD` bind shim | Unnecessary given the above. |

---

## Connector implementation notes (Phase 1)

Derived from IL analysis of `OpenSDKv1`. These bite on **any** port, 921 included.

- **Framing.** One `Read()` into a fresh zero-filled buffer sized to `SO_RCVBUF`, ASCII-decoded
  whole and handed to Newtonsoft. **One JSON object per `write()` — never pipeline two.**
  Trailing NULs are tolerated.
- **Responses are unframed too** — 201 and 202 routinely arrive coalesced. Use a streaming
  `json.Decoder`.
- **Heartbeats get no reply.** The Code 200 ack is gated on `(ClubDataLoaded || BallDataLoaded)`.
  A request/await-ack client blocks forever on a heartbeat.
- **Code 200 is asynchronous and coalescing** — emitted from a 200 ms UI timer via a single
  `ShotDataFound` bool. Two shots <200 ms apart yield ONE ack. Cap at ~5 shots/sec.
- **Undocumented codes:** 202 "GSPro ready", 203 "GSPro round ended". Don't treat unknown
  codes as fatal.
- **Always send a non-null `ShotDataOptions`, and never a top-level `data` object.** A payload
  that fails the `ShotDataOptions` check then parses as `MinShotData` latches
  `OpenAPIv1IsType=false` for the life of the connection — all five response emitters go
  permanently silent.
- **Send a heartbeat first.** The 41491 brand announcement fires only after the first
  successfully-parsed message; until then GSPro's `lmData.LMtype` is null and
  `CheckOffsetSettings` dereferences it with no null guard.
- **Spin is order-sensitive:** send `{SpinAxis, TotalSpin}` **or** `{BackSpin, SideSpin}`,
  never a half-filled mix. `BackSpin=0` with `SideSpin!=0` divides by zero into `atan(inf)`.
- **Ignored fields:** `Units` (comes from GSPconnect's own `CarryMetric` setting),
  `APIversion`, `ShotNumber`, `ClubIndex` — parsed, never read.
- **Reconnect is broken upstream and this matters most on a phone.** GSPconnect never checks
  `Read()`'s return value, so on a client FIN it hot-spins on a zeroed buffer instead of
  re-accepting; on RST the cleanup is guarded on `connectedTcpClient.Connected` (already
  false). **Hold one long-lived connection and never half-close it.** Observed directly: an
  abruptly-killed client left 196 bytes unread in `CLOSE_WAIT` and wedged the listener.
  Field recovery lever: changing club in GSPro forces an outbound write that throws and
  triggers `CleanUpConnection2LMKill`.
- **The connector UI lies when the link dies** — `clientConnectedOSDK` is never cleared on a
  plain client disconnect, so it still reads "Connected". Don't trust it; use our own
  liveness.

### Security note

`OpenSDKv1` uses `new TcpListener(int)` — the **`IPAddress.Any`** overload — unlike Simple
Control (`127.0.0.1:58822`) and Open Control (`127.0.0.1:9090`), which bind loopback. So
**18921 is reachable by anyone on the same Wi-Fi, unauthenticated.** Fine on a home network;
worth knowing on a public one.

---

## Impact on the plan

- **`PROJECT-BRIEF.md`** — architecture unchanged and validated. Update the default from
  `127.0.0.1:921` to `127.0.0.1:18921`. CLAUDE.md already forbids hardcoding, so this is a
  settings default.
- **`BOOTSTRAP.md`** — the Phase 0 hand-test is done; the gate is passed. Sessions 1–3
  proceed unchanged.
- Delete the Go-relay fallback section. The failure mode it addressed does not exist.
