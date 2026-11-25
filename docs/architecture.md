# IEC 60870-5-104 Client — Architecture

Design notes for [`iec104_client/`](../iec104_client) (see also the root
[`README.md`](../README.md) and the library report
[`iec104_library_bug_report.md`](iec104_library_bug_report.md)).

- **Library:** [`wendy512/go-iecp5`](https://github.com/wendy512/go-iecp5) v1.2.5
  (`cs104` transport + ASDU codec), Go 1.27.1, module
  `github.com/alfas/iec104-client`. `wendy512/iec104` is no longer used.
- **Transport:** this program is the **TCP client**. It dials MSL5 (IEC 104
  server, default port 2404) and reconnects forever.
- **Role:** on the application level the client is the *controlling* side: it
  sends `C_*` commands and receives MSL5's `M_*` monitoring frames (direction
  inverted by MSL5 commit `ea8a09c1`; see
  [`../VK_IEC104_MIGRATION.md`](../VK_IEC104_MIGRATION.md)).

---

## 1. Scope

```text
  VK (this repo)                                     MSL5 (IEC 104 server)
  TCP client                                         TCP server
      ── TCP connect ───────────────────────────────►
      ── STARTDT act ───────────────────────────────►
      ◄─ STARTDT con ────────────────────────────────
      ── C_* COT 6 (activation, every -cmd-interval) ►   [104-OUT] stored, channel written
      ◄─ C_* COT 7 (ActCon, mirrored by the library) ─
      ◄─ M_* COT 3 (cyclic monitoring) ──────────────    [104-IN] stored in the image by CA + IOA
```

Only `C_*` commands (out) and `M_*` monitoring frames (in) exist. The client never
sends `M_*` and never answers confirmations. General interrogation,
counter interrogation, read, clock sync, test, reset process and delay
acquisition are out of scope: the handlers log (with `-log-frames`) and return `nil`,
never answering.

## 2. MSL5 contract

MSL5 is a stock IEC 104 server; this client must satisfy the following
(implemented and tested on the MSL5 side).

* **One session per MSL5 (newest wins).** A new TCP connection replaces the old
  one, so run exactly one client instance; with auto-reconnect two instances
  would kick each other out.
* **Wire parameters.** `asdu.ParamsWide` (COT 2 bytes incl. originator address,
  CA 2 bytes, IOA 3 bytes) and the `cs104.DefaultConfig()` timers (t0 30 s,
  t1 15 s, t2 10 s, t3 20 s, k 12, w 8). MSL5 sends `TESTFR` after t3 of
  silence; `cs104.Client` answers it automatically.
* **STARTDT comes from this side**, and the library drops every I-frame before
  it.
* **Commands (VK → MSL5).** MSL5 accepts only `C_SC_NA_1`, `C_DC_NA_1`,
  `C_RC_NA_1`, `C_SE_NA_1`, `C_SE_NB_1`, `C_SE_NC_1`, `C_BO_NA_1` with COT 6, one
  object per ASDU. Other causes are dropped silently, other types are rejected
  and the server library mirrors them back as COT 44 (`UnknownTypeID`) — which
  is what the old `M_*` reporter provoked. `C_SE_NA_1` carries a **raw int16**.
  Type IDs 58–64 are never sent.
* **Confirmations.** MSL5 mirrors every accepted command as `ActivationCon`
  (COT 7). The client records COT 7/10 but never executes or answers them.
* **Monitoring (MSL5 → VK).** MSL5 sends each active IN row as one object per
  ASDU with COT 3: `M_SP_NA_1`, `M_DP_NA_1`, `M_ME_NC_1`, `M_ME_TF_1`,
  `M_ME_NB_1`, `M_IT_NA_1`, for any CA; the client keys them by CA + IOA.

## 3. Files

| File | Responsibility |
| :-- | :-- |
| `main.go` | flags (`-msl5-host`, `-msl5-port`, `-ca`, `-cmd-interval`, `-cmd-on-change`, `-reconnect`, `-duration`, `-log-frames`, `-verbose`), `newClient()` wiring, shutdown on `-duration` or SIGINT/SIGTERM |
| `client_handler.go` | `clientHandler`: `cs104.ClientHandlerInterface` adapter; `ASDUHandler` → `station.OnASDU`, all other handlers ignore |
| `handler.go` | `station` = monitoring image keyed by CA + IOA, recorded confirmations, `OnASDU` |
| `confirm.go` | `coa()` and `newCommandASDU()` (fresh single-object ASDU) |
| `sender.go` | `sender`: command points, Start/Stop/Kick/Set, `sendAll`, send-error handling |
| `handler_test.go`, `loopback_test.go` | unit tests (capture connection) and loopback tests against an MSL5-like `cs104.Server` |

## 4. Session lifecycle

1. `cs104.Client.Start()` returns immediately; the library dials, and on failure
   or loss reconnects every `-reconnect` (plus ~0.5–1 s).
2. `SetOnConnectHandler` → `SendStartDt()`. MSL5 never sends STARTDT, and the
   library drops I-frames before it.
3. `SetServerActiveHandler` fires when STARTDT is confirmed → `sender.Start()`.
   `Send` returns `ErrNotActive` before that, so nothing is sent earlier.
4. `SetConnectionLostHandler` → `sender.Stop()`. Both callbacks can fire many
   times; `Start`/`Stop` are idempotent and goroutine-safe (`Stop` waits for the
   loop to exit).
5. Routing of received frames: `C_IC/C_CI/C_RD/C_CS/C_TS/C_RP/C_CD` go to the
   dedicated (ignoring) handlers, everything else, including all `M_*` and the
   mirrored `C_*` confirmations, goes to `ASDUHandler`. Handlers run on one
   goroutine per session and must not block.
6. Logging: default output is lifecycle only. `-log-frames` prints one `TX` or
   `RX` line per ASDU (sent `C_*`, received `M_*`, confirmations, ignored
   types); `-verbose` implies it and additionally enables the go-iecp5 library
   log via `cli.LogMode(true)` (raw APDUs, frame bookkeeping, decode errors).
   The systemd unit passes `-log-frames`.

MSL5 keeps one session (newest wins): run exactly one instance per MSL5.

## 5. Why every outgoing frame is a brand-new ASDU

The `Get*Cmd()` getters consume the ASDU's information-object buffer, so
`SendReplyMirror()` after parsing would send a truncated body. The client
therefore never mirrors: commands are built with the library builders (COT 6) or
`newCommandASDU`, and the handler never returns an error for received frames
(the server-side library would otherwise mirror them back as COT 44).

## 6. Monitoring image, sender and concurrency

```go
type station struct {
	mu            sync.Mutex
	monitoring    map[pointKey]monitoringValue // M_* by (CA, IOA)
	confirmations map[pointKey]confirmation    // last COT 7 / 10
	logFrames     bool
}
```

* `OnASDU` decodes the six `M_*` types with the `Get*` helpers (slices, `SQ`-aware)
  and keys them by the frame's CA. Received data never triggers a send, so MSL5's
  `ActCon` mirror cannot loop.
* `sender` holds the command points (`defaultCommandPoints()`: IOA 100 `C_SC`,
  200 `C_DC`, 250 `C_RC`, 301 `C_SE_NA`, 303 `C_SE_NB`, 302 `C_SE_NC`, 400 `C_BO`).
  Its mutex guards the points and is **never held during `Send`**: `sendAll`
  snapshots the points, releases the lock, then sends.
* The loop runs while the session is active: a round on start and every
  `-cmd-interval` (none when the interval is 0), and after `Set()` via `Kick` with a
  100 ms debounce when `-cmd-on-change` is set. `Kick` is a non-blocking write to
  a 1-buffered channel.
* A failed send aborts the round: `ErrNotActive`/`ErrUseClosedConnection`
  silently, `ErrBufferFulled` with a rate-limited warning.

## 7. Common address policy

Outgoing commands use `-ca`. Incoming `M_*` frames are accepted for any CA and
stored under the CA they carry.

## 8. Testing approach

* `captureConn` implements `asdu.Connect` and records marshalled frames; `request()`
  builds a frame with the library helpers and decodes it again, giving
  handlers the same "consumed" ASDU they see in production.
* Unit tests cover the monitoring image, confirmations (never executed or
  answered), frame-log gating (`-log-frames` off/on), ignored requests, the
  seven command types (COT 6, CA, IOA, value), send-on-change, sender lifecycle
  and send errors.
* `loopback_test.go` runs the real `newClient` wiring against an MSL5-like
  `cs104.Server` on a free `127.0.0.1` port: STARTDT, commands mirrored as
  ActCon, monitoring image, no command loop, and resume after a server restart.
  The race detector needs cgo (`go test -race`), which is not available on every
  development machine.

## 9. Packaging and deployment

| Path | Purpose |
| :-- | :-- |
| `build.sh` | `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w"` → `dist/iec104-client` |
| `deploy/iec104-client.service` | systemd unit: `User=iec104`, `ExecStart=/opt/iec104/iec104-client -msl5-host 127.0.0.1 -msl5-port 2404 -ca 1`, `Restart=always`, `RestartSec=2` |
| `deploy/install.sh` | creates the `iec104` user, installs binary + unit, removes the legacy outstation-server unit/binary, `enable --now` |
| `deploy/uninstall.sh` | stops/disables the unit (and the legacy server unit), removes unit files and `/opt/iec104` |
| `pull.sh` | token-authenticated `git fetch` + `--ff-only` merge on the Pi |

The program does not listen, so it can share a device with MSL5, which owns
port 2404.

## 10. Library limitations

`asdu.infoObjSize` lacks Type IDs 58–64 (`C_*_TA/TB/TC`): the library drops such
frames before any handler runs. MSL5 never sends them and the client never sends
them either. See
[`iec104_library_bug_report.md`](iec104_library_bug_report.md).

## 11. Open items

* Command values are static defaults (`defaultCommandPoints()`) apart from
  `sender.Set()`; wiring them to real local inputs is out of scope.
