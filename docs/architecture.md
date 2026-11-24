# IEC 60870-5-104 Client (Outstation) — Architecture

Design notes for [`iec104_client/`](../iec104_client) (see also the root
[`README.md`](../README.md) and the library report
[`iec104_library_bug_report.md`](iec104_library_bug_report.md)).

- **Library:** [`wendy512/go-iecp5`](https://github.com/wendy512/go-iecp5) v1.2.5
  (`cs104` transport + ASDU codec), Go 1.27.1, module
  `github.com/alfas/iec104-client`. `wendy512/iec104` is no longer used.
- **Transport:** this program is the **TCP client**. It dials MSL5 (IEC 104
  server, default port 2404) and reconnects forever.
- **Role:** *controlled station* (outstation) on the application level: it owns
  the process image, reports it spontaneously and answers MSL5's control
  commands.

---

## 1. Scope

```text
  VK (this repo)                                     MSL5 (IEC 104 server)
  TCP client, controlled station                     TCP server, controlling station
      ── TCP connect ───────────────────────────────►
      ── STARTDT act ───────────────────────────────►
      ◄─ STARTDT con ────────────────────────────────
      ── M_* COT 3 (spontaneous, full image) ───────►   matched by CA + IOA + type
      ◄─ C_* COT 6 (activation, cyclic) ─────────────
      ── C_* COT 7 (ActCon), C_* COT 10 (ActTerm) ──►   stored on the IN row
      ── M_* COT 3 (report of the changed image) ───►
```

Only spontaneous/cyclic data and control commands exist. General interrogation,
counter interrogation, read, clock sync, test, reset process and delay
acquisition are out of scope: the handlers log (with `-verbose`) and return `nil`,
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
  it. MSL5 reports `connected` on TCP accept and `active` after the first
  received ASDU, so the first report follows the STARTDT confirmation.
* **OUT rows (monitoring).** MSL5 accepts a frame only if its cause is
  Spontaneous(3) or Periodic(1) *and* an active OUT row matches
  `CA + IOA(-Ret) + type`. A match is stored in the row's `responsePayload` and
  written to the linked SPI channel (`M_SP`/`M_DP` → digital out, measurements
  and `M_IT_*` → analog out; double point 2/3 is stored but not written).
  `M_ME_NA_1` cannot be configured in MSL5 v1 and is never reported.
* **IN rows (commands).** MSL5 sends each active row's command cyclically with
  COT 6, one information object per ASDU (SQ=0): `C_SC_NA_1`, `C_DC_NA_1`,
  `C_SE_NA_1`, `C_SE_NB_1`, `C_SE_NC_1`, `C_BO_NA_1`. `C_SE_NA_1` carries a
  **raw int16**, not a /32768 normalised float. Type IDs 58–64, `C_RC`, `C_IC`,
  `C_CI`, `C_RD`, `C_CS`, `C_TS` and `C_RP` are never sent.
* **Confirmations.** MSL5 matches a `C_*` frame to an IN row by
  `CA + IOA + exact command type` and keeps the last one, so confirmations echo
  the request's CA and `ActivationCon`(7) is followed by `ActivationTerm`(10).
* **Nothing else to answer.** MSL5 never sends a negative confirmation or an
  unknown-IOA reply; the types it ignores (`C_IC`, `C_CI`, `C_RD`, `C_CS`,
  `C_RP`, `C_CD`) are logged here and left unanswered as well.

## 3. Files

| File | Responsibility |
| :-- | :-- |
| `main.go` | flags (`-msl5-host`, `-msl5-port`, `-ca`, `-spont-interval`, `-cyclic`, `-reconnect`, `-duration`, `-verbose`), `newClient()` wiring, shutdown on `-duration` or SIGINT/SIGTERM |
| `client_handler.go` | `clientHandler`: `cs104.ClientHandlerInterface` adapter; `ASDUHandler` → `station.OnASDU`, all other handlers ignore |
| `handler.go` | `station` = process image, `OnASDU` command dispatch, `apply*`, ActCon + ActTerm |
| `confirm.go` | hand-built ASDUs: `confirm*` command echoes (cause is a parameter), `sendMeasuredFloatTime`, `sendCounterRequest`, `coa()`, `newCommandASDU()` |
| `reporter.go` | `reporter`: Start/Stop/Kick, `reportAll`, send-error handling |
| `handler_test.go`, `loopback_test.go` | unit tests (capture connection) and a loopback test against a real `cs104.Server` |

## 4. Session lifecycle

1. `cs104.Client.Start()` returns immediately; the library dials, and on failure
   or loss reconnects every `-reconnect` (plus ~0.5–1 s).
2. `SetOnConnectHandler` → `SendStartDt()`. MSL5 never sends STARTDT, and the
   library drops I-frames before it.
3. `SetServerActiveHandler` fires when STARTDT is confirmed → `reporter.Start()`.
   `Send` returns `ErrNotActive` before that, so nothing is sent earlier.
4. `SetConnectionLostHandler` → `reporter.Stop()`. Both callbacks can fire many
   times; `Start`/`Stop` are idempotent and goroutine-safe (`Stop` waits for the
   loop to exit).
5. Routing of received frames: `C_IC/C_CI/C_RD/C_CS/C_TS/C_RP/C_CD` go to the
   dedicated (ignoring) handlers, everything else, including all
   `C_SC/C_DC/C_SE/C_BO`, goes to `ASDUHandler`. Handlers run on one goroutine
   per session and must not block.

MSL5 keeps one session (newest wins): run exactly one instance per MSL5.

## 5. Why every answer is a brand-new ASDU

The `Get*Cmd()` getters consume the ASDU's information-object buffer, so
`SendReplyMirror()` after parsing would send a truncated body. Therefore:

1. parse with `pack.Get*Cmd()` only to read parameters;
2. store the effect in the process image;
3. build a fresh ASDU (`newCommandASDU` + `AppendInfoObjAddr` + `Append*`) with
   the requested cause and the **request's** CA, and `conn.Send` it — first
   `ActivationCon`, then `ActivationTerm`;
4. never return a send error to the library: `replyOrLog` logs it and the handler
   returns `nil`.

Hand-building is also required where library helpers restrict the cause
(`BitsString32Cmd`, `MeasuredValueFloatCP56Time2a`, `IntegratedTotals`).

## 6. Process image, reporter and concurrency

```go
type station struct {
	mu       sync.Mutex
	spVal    bool             // IOA 100
	dpVal    asdu.DoublePoint // IOA 200
	ana      float32          // IOA 301 / 302 / 303
	counter  int32            // IOA 400
	bits     uint32           // last C_BO_* command (not reported)
	onChange func()           // reporter.Kick, called outside mu
}
```

* `st.mu` protects the image and is **never held during `Send`**: `reportAll`
  snapshots the image, releases the lock, then sends.
* The reporter loop runs while the session is active: full image on start, on
  every `-spont-interval` tick (the counter is incremented first), and after a
  command via `Kick` with a 100 ms debounce. `Kick` is a non-blocking write to a
  1-buffered channel.
* Frames (COT 3, CA `-ca`, one object per ASDU): `M_SP_NA_1` 100, `M_DP_NA_1` 200
  (0/1 only), `M_ME_NC_1` 301, `M_ME_TF_1` 302, `M_ME_NB_1` 303 (clamped int16),
  `M_IT_NA_1` 400. `M_ME_NA_1` is never reported. With `-cyclic` only
  `M_ME_NC_1`/`M_ME_NB_1` use COT 1 (the other helpers reject it).
* A failed send aborts the report round: `ErrNotActive`/`ErrUseClosedConnection`
  silently, `ErrBufferFulled` with a warning.

## 7. Common address policy

Spontaneous frames use `-ca` (one CA for all of them). Command confirmations echo
`pack.CommonAddr`, because MSL5 matches IN rows by CA + IOA + command type.

## 8. Testing approach

* `captureConn` implements `asdu.Connect` and records marshalled frames; `request()`
  builds a frame with the library's `asdu.*Cmd` helpers and decodes it again, giving
  handlers the same "consumed" ASDU they see in production.
* Unit tests cover commands (exactly ActCon then ActTerm, request CA, image
  update, report kick), ignored requests, the six reported frames, image values,
  `-cyclic`, reporter lifecycle under `-race`, and send errors.
* `loopback_test.go` runs the real `newClient` wiring against a `cs104.Server` on
  a free `127.0.0.1` port: STARTDT, spontaneous image, command → ActCon/ActTerm →
  changed report, and reconnect to a restarted server.

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
frames before any handler runs. MSL5 never sends them; the tagged branches stay in
`OnASDU` for a patched library. See
[`iec104_library_bug_report.md`](iec104_library_bug_report.md).

## 11. Open items

* `C_RC_*` (step commands) are logged as unhandled, not confirmed.
* The image is static apart from commands and the `M_IT` counter; wiring it to
  real I/O is out of scope.
