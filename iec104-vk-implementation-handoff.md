# IEC 60870-5-104 — VK outstation becomes a dialling client (implementation handoff)

**Target repository:** `IEC104_Go` (`https://github.com/Mhd25OrCode/IEC104_Go`), folder `iec104_server`
**Counterpart:** MSL5 on branch `iec-104-new` (already implemented, see [ADR-0005](adr/0005-iec104-msl5-als-server.md))
**Audience:** the developer who changes the VK repository. This document is self-contained: you do not need to read the MSL5 code. Where MSL5 behaviour matters it is stated here, with the MSL5 file that proves it.

---

## 1. Goal in one paragraph

Until now VK was an IEC 104 **server** (listening on 2404) that MSL5 dialled as a client and polled with `C_RD_NA_1`. The roles are reversed. **MSL5 is now the IEC 104 server** and listens on a TCP port. **VK becomes a dummy IEC 104 client**: it dials MSL5, issues STARTDT, **sends spontaneous `M_*` frames** (COT 3) and **answers the control commands MSL5 sends cyclically** (`C_SC`, `C_DC`, `C_SE`, `C_BO`) with `ActivationCon` followed by `ActivationTerm`. Only spontaneous/cyclic data and control commands exist: **no general interrogation, no counter interrogation, no `C_RD_*`**.

VK stays the *controlled station* on the application level (it owns the process image, MSL5 commands it). Only the TCP direction is inverted. That is legal; it is simply unusual, so do not "fix" it.

```
          TCP connect (VK dials)                  VK = TCP client, controlled station
  VK  ─────────────────────────────────►  MSL5    MSL5 = TCP server, controlling station
      ── STARTDT act ──►                           (the library lets VK send STARTDT)
      ◄─ STARTDT con ───
      ── M_* COT 3 (spontaneous) ──►   [104-OUT]   MSL5 matches CA+IOA+type, stores it, writes the SPI
      ◄─ C_* COT 6 (activation) ─────  [104-IN]    cyclic, interval per row in MSL5
      ── C_* COT 7 (ActCon) ──►                    MSL5 stores it on the IN row
      ── C_* COT 10 (ActTerm) ──►
```

## 2. What does NOT change / what to delete

Keep: the process image (`station`), the apply logic (`applySingle/Double/Analog/Bits`), the confirmation builders in `confirm.go`, the `captureConn`-based unit tests, the build/deploy scripts.

Delete (out of scope, and they would be dead code for a client):

| Remove | Where today |
|---|---|
| General interrogation: `OnInterrogation`, `sendAllData`, `confirmInterrogation` | `handler.go:39-107`, `confirm.go` |
| Counter interrogation: `OnCounterInterrogation`, `sendCounters`, `confirmCounterInterrogation` | `handler.go:72-125`, `confirm.go` |
| Read: `OnRead`, `confirmReadError` (`C_RD_NA_1`, COT 5/47) | `handler.go:131-162`, `confirm.go:245` |
| `OnClockSync`, `OnResetProcess`, `OnDelayAcquisition`, `OnTestCommand` and their `confirm*` | `handler.go:165-195`, `confirm.go` |
| Import of `github.com/wendy512/iec104/server` and the whole `server.New(...)` wiring | `main.go` |
| Tests for the above (`TestReadAnswersWithNativeTypes`, `TestReadUnknownIOA`, `TestReadBackAfterCommand`, `TestInterrogation…`, `TestCounterInterrogation…`, `TestCommandsAndReadsWorkWithoutInterrogation`) | `handler_test.go` |

If a peer ever sends one of these types, log it at debug level and return `nil` (see §5.3). Do not answer it.

## 3. Wire parameters (must match MSL5)

MSL5 uses the library default `asdu.ParamsWide`: **COT 2 bytes (with originator address), CA 2 bytes, IOA 3 bytes**. `cs104.NewOption()` already defaults to it; do not change it.

| Item | Value |
|---|---|
| Transport | TCP, MSL5 default port `2404` (configurable in MSL5) |
| Common Address (CA) | VK flag `-ca`, default `1`. A row in MSL5 matches the CA exactly (`asduHigh<<8 \| asduLow`, never 0). VK must use **one CA for all spontaneous frames**; in confirmations VK **echoes the CA of the received command** (as it does today). |
| IOA | 3 bytes (`ioaHigh<<16 \| ioaMiddle<<8 \| ioaLow`), never 0 |
| Timers | library defaults of `cs104.DefaultConfig()` (t0 30 s, t1 15 s, t2 10 s, t3 20 s, k 12, w 8). Keep them. |
| Keep-alive | MSL5 sends `TESTFR act` after t3 of silence; `cs104.Client` answers it automatically. |

## 4. What MSL5 does (the contract VK must satisfy)

All of this is implemented and unit/loopback tested in MSL5 (`src/packages/iec104`, `src/apps/backend/internal/device/iec104/service`).

1. **One session, newest wins.** A new TCP connection replaces the old one (the old one is closed). Consequence for VK: **run exactly one VK instance per MSL5.** With `AutoReconnect` two instances kick each other out every ~1 s.
2. **STARTDT is mandatory and comes from VK.** The library drops every I-frame that arrives before STARTDT (`station not active`). MSL5 never sends STARTDT. MSL5 reports the session as `connected` after TCP accept and as `active` once it has **received its first ASDU** — so VK should send a first spontaneous frame right after STARTDT is confirmed (see §7).
3. **104-OUT (spontaneous frames).** For each received `M_*` information object MSL5 requires, in this order:
   1. cause of transmission is **3 (Spontaneous)** or **1 (Periodic/cyclic)**, not negative, not test (everything else, e.g. 5 or 20..36, is silently dropped);
   2. a configured, **active** OUT row with `CA == frame CA`, `IOA-Ret == frame IOA` (or the row's IOA when IOA-Ret is empty) and `ASDU-Ret type == frame type`.

   On a match it stores the frame in the row's `responsePayload` and writes the value to the linked SPI channel: `M_SP/M_DP` → digital out (double point 2/3 is stored but not written), `M_ME_NB/NC/TB/TC/TE/TF` and `M_IT_*` → analog out. **Every** matching frame is applied, not only the first. Normalised measurements (`M_ME_NA/TA/TD/ND`) cannot be configured in MSL5 v1 — **do not report `M_ME_NA_1`** (it would be ignored).
4. **104-IN (commands).** MSL5 sends each active IN row's command cyclically (row interval `5 sek … 1 tag`, default 5 min) plus on manual "Senden". Sent with **COT 6 (Activation)**, the row's CA/IOA, one information object per ASDU (SQ=0). Types and value kinds:

   | Type (ID) | Value on the wire | Notes |
   |---|---|---|
   | `C_SC_NA_1` (45) | bool | single command |
   | `C_DC_NA_1` (46) | uint8 0..3 | double command |
   | `C_SE_NA_1` (48) | int16 | **raw int16, not a /32768 normalised float** — store it raw (VK already does) |
   | `C_SE_NB_1` (49) | int16 | scaled |
   | `C_SE_NC_1` (50) | float32 | short float |
   | `C_BO_NA_1` (51) | uint32 | bitstring |

   Never sent: type IDs 58–64 (`C_*_TA/TB/TC`), `C_RC`, `C_IC`, `C_CI`, `C_RD`, `C_CS`, `C_TS`, `C_RP`. (The library cannot decode 58–64, MSL5 refuses to configure them.)
5. **Command confirmations.** MSL5 matches an incoming `C_*` frame to the IN row by **CA + IOA + exact command type** and stores it in `responsePayload` (the payload contains `cot.cause`, so ActCon 7 and ActTerm 10 are both visible; the last one wins). Frames that do not match (other type at that IOA, unknown IOA) are dropped.
6. **Ignored on purpose.** `C_IC_NA_1`, `C_CI_NA_1`, `C_RD_NA_1`, `C_CS_NA_1`, `C_RP_NA_1`, `C_CD_NA_1` from VK are logged and **not answered**. `C_TS_NA_1` from VK would be auto-confirmed by the library; do not send it.
7. **Library quirk that affects VK's answers.** If MSL5's frame handler returned an error, the library would mirror the frame back with COT 44. MSL5 always returns `nil`, so VK will never see that. But also note: **MSL5 never sends a negative confirmation or an unknown-IOA reply** — there is nothing for VK to handle there.

## 5. Changes in the VK repository, file by file

### 5.1 `main.go` — dial instead of listen

Replace `server.New(...)`/`s.Start()` with a `cs104.Client` (package `github.com/wendy512/go-iecp5/cs104`). **Do not use `github.com/wendy512/iec104/client`**: its wrapper discards the connection in every callback and gives the `ASDUCall` only `*asdu.ASDU`, so VK could not answer commands.

Flags (replace `-host`/`-port`):

| Flag | Default | Meaning |
|---|---|---|
| `-msl5-host` | `127.0.0.1` | MSL5 address to dial |
| `-msl5-port` | `2404` | MSL5 IEC 104 port |
| `-ca` | `1` | Common Address of all spontaneous frames |
| `-spont-interval` | `5s` | period of the full spontaneous report; `0` = only on change/connect |
| `-cyclic` | `false` | additionally send measurements with COT 1 (see §7.3) |
| `-reconnect` | `2s` | delay between reconnect attempts |
| `-duration`, `-verbose` | as today | `-verbose` → `LogMode(true)` on the client so decode errors are visible |

Skeleton (API verified against go-iecp5 v1.2.5):

```go
opt := cs104.NewOption()                       // ParamsWide, DefaultConfig, AutoReconnect=true
opt.SetReconnectInterval(*reconnect)
if err := opt.AddRemoteServer(net.JoinHostPort(*msl5Host, strconv.Itoa(*msl5Port))); err != nil { log.Fatal(err) }

st := &station{ /* power-on image as today */ }
cli := cs104.NewClient(&clientHandler{st: st}, opt)
cli.LogMode(*verbose)

rep := newReporter(st, cli, asdu.CommonAddr(*ca), *spontInterval, *cyclic)
cli.SetOnConnectHandler(func(c *cs104.Client) { c.SendStartDt() })        // §6
cli.SetServerActiveHandler(func(c *cs104.Client) { rep.Start() })          // STARTDT confirmed → report
cli.SetConnectionLostHandler(func(c *cs104.Client) { rep.Stop() })

if err := cli.Start(); err != nil { log.Fatal(err) }   // returns immediately, reconnects in the background
// ... wait for signal / -duration, then rep.Stop(); cli.Close()
```

`cs104.Client` implements `asdu.Connect` (`Params`, `Send`, `UnderlyingConn`), so every existing `confirm*` helper that takes an `asdu.Connect` works unchanged with the client as `conn`.

### 5.2 New `client_handler.go` — the ClientHandlerInterface adapter

`cs104.ClientHandlerInterface` has eight methods. The command handler you already have (`station.OnASDU(conn, pack) error`) has exactly the signature of `ASDUHandler`, so adapt instead of rewriting:

```go
type clientHandler struct{ st *station }

func (h *clientHandler) ASDUHandler(c asdu.Connect, p *asdu.ASDU) error { return h.st.OnASDU(c, p) }

// Out of scope: log at debug level and return nil. Never answer.
func (h *clientHandler) InterrogationHandler(asdu.Connect, *asdu.ASDU) error        { return nil }
func (h *clientHandler) CounterInterrogationHandler(asdu.Connect, *asdu.ASDU) error { return nil }
func (h *clientHandler) ReadHandler(asdu.Connect, *asdu.ASDU) error                 { return nil }
func (h *clientHandler) TestCommandHandler(asdu.Connect, *asdu.ASDU) error          { return nil }
func (h *clientHandler) ClockSyncHandler(asdu.Connect, *asdu.ASDU) error            { return nil }
func (h *clientHandler) ResetProcessHandler(asdu.Connect, *asdu.ASDU) error         { return nil }
func (h *clientHandler) DelayAcquisitionHandler(asdu.Connect, *asdu.ASDU) error     { return nil }
```

How the client routes frames (`cs104/client.go:509`): `C_IC/C_CI/C_RD/C_CS/C_TS/C_RP/C_CD` go to the dedicated handlers above, **everything else — including all C_SC/C_DC/C_SE/C_BO commands — goes to `ASDUHandler`**. The handlers run on one goroutine per session: keep them fast, do not block on `Send`.

### 5.3 `handler.go` — command handling

`OnASDU` keeps its switch for `C_SC_NA_1`, `C_DC_NA_1`, `C_SE_NA_1`, `C_SE_NB_1`, `C_SE_NC_1`, `C_BO_NA_1` (the `_TA/_TB/_TC` variants may stay; MSL5 never sends them). Changes:

1. **Send ActCon, then ActTerm.** For each command: apply to the image, send `ActivationCon` (COT 7), then `ActivationTerm` (COT 10) — same type, same CA, same IOA, same value. MSL5 stores both; the IN row finally shows ActTerm.
2. **Build fresh ASDUs; never `SendReplyMirror`.** go-iecp5's `Get*Cmd()` getters consume the info-object buffer, so a mirror built after parsing is truncated. `confirm.go` already follows this rule; keep it.
3. **Echo the request CA** (`pack.CommonAddr`), not the `-ca` flag. (Spontaneous frames use `-ca`.)
4. **Trigger a report after applying a command** (§7.2) so the effect loops back to MSL5 as a spontaneous frame — this is what makes an IN→OUT round trip visible.
5. Keep the `replyOrLog` pattern: a failed `Send` is logged, the handler returns `nil`.
6. `Cause` handling: MSL5 only sends Activation (6). If `pack.Coa.Cause == asdu.Deactivation` you may ignore the frame (log); it is not needed.

### 5.4 `confirm.go` — add the cause parameter

Every `confirm<Type>Cmd(conn, typeID, ca, cmd)` hard-codes `asdu.ActivationCon`. Change the signature to take the cause:

```go
func confirmSingleCmd(conn asdu.Connect, typeID asdu.TypeID, cause asdu.Cause, ca asdu.CommonAddr, cmd asdu.SingleCommandInfo) error {
    u := newCommandASDU(conn, typeID, cause, ca)      // was: asdu.ActivationCon
    ...
}
```

Apply the same change to `confirmDoubleCmd`, `confirmSetpointNormal`, `confirmSetpointScaled`, `confirmSetpointFloat`, `confirmBitsString32Cmd`. In `OnASDU` call each twice: `asdu.ActivationCon`, then `asdu.ActivationTerm`. Delete the helpers listed in §2.

### 5.5 New `reporter.go` — spontaneous reporting

See §7 for the point table, rules and a skeleton.

### 5.6 `go.mod`

After the changes `github.com/wendy512/iec104` is no longer imported: run `go mod tidy`. Only `github.com/wendy512/go-iecp5 v1.2.5` remains. (`go 1.27.1` in the file stays as it is.)

### 5.7 Deployment and docs

- `deploy/iec104-server.service`: change `Description=` to "IEC 60870-5-104 outstation (client, dials MSL5)" and `ExecStart=/opt/iec104/iec104-server -msl5-host 127.0.0.1 -msl5-port 2404 -ca 1`. `Restart=always`/`RestartSec=2` stay (the library also reconnects by itself, the unit is the safety net). The binary and unit may keep their old name to avoid touching `install.sh`; a rename is optional.
- **Port conflict is gone:** VK no longer listens, so it can run on the same device as MSL5, which now owns 2404.
- `README.md` (header, "No unsolicited traffic" section around l.154-160, flag table l.58-59, run examples l.43-46 and l.412) and `docs/architecture.md` (§1, §3, §11): update to the client role, the new flags and the spontaneous reporting. The two copied MSL5 plan files in `docs/` are outdated: add a one-line "superseded" note or delete them. `IEC104_SERVER_ANALYSIS_AND_RECOMMENDATIONS.md` stays as history; its findings on GI/CI/RD/CS are no longer relevant to this scope.

## 6. Connection handling and STARTDT

- `cli.Start()` returns immediately; the library dials and **reconnects forever** (`AutoReconnect` default true, interval `-reconnect`). VK therefore needs no dial loop of its own. While MSL5 is down, VK just logs `connect failed`.
- **VK must send STARTDT**: `SetOnConnectHandler(func(c){ c.SendStartDt() })`. The library's client side only handles the STARTDT *confirmation*; MSL5's session answers `STARTDT con` and from then on delivers its commands.
- `cs104.Client.Send` returns `ErrNotActive` until the confirmation arrived. `SetServerActiveHandler` fires exactly then (the library invokes it in its own goroutine) — **start the reporter there, stop it in `SetConnectionLostHandler`**. Both callbacks can fire many times (reconnects): `Start`/`Stop` must be idempotent and safe to call from different goroutines.
- If MSL5 closes the session (e.g. a second client connected, or MSL5 was reconfigured) the library reconnects after ~0.5–1 s plus `-reconnect`. That is expected.
- Errors from `Send`: `ErrNotActive`, `ErrUseClosedConnection` (session gone) → drop silently and wait for the next tick; `ErrBufferFulled` (192 ASDUs queued) → log a warning and skip. Never loop on a failed send.

## 7. Spontaneous reporting

### 7.1 Points to report

Keep the existing process image; report these, all with **COT 3 (Spontaneous)**, `CA = -ca`, SQ=0, one object per ASDU:

| IOA | Type | Value source | MSL5 `ASDU-Ret Typ` | MSL5 writes to |
|---|---|---|---|---|
| 100 | `M_SP_NA_1` | `st.spVal` | `M_SP_NA_1` | digital out |
| 200 | `M_DP_NA_1` | `st.dpVal` (use 0/1; 2/3 are never written by MSL5) | `M_DP_NA_1` | digital out |
| 301 | `M_ME_NC_1` | `st.ana` (float32) | `M_ME_NC_1` | analog out |
| 302 | `M_ME_TF_1` | `st.ana`, CP56Time2a = now | `M_ME_TF_1` | analog out |
| 303 | `M_ME_NB_1` | `int16(st.ana)` (clamped) — *new IOA* | `M_ME_NB_1` | analog out |
| 400 | `M_IT_NA_1` | `st.counter` | `M_IT_NA_1` | analog out |

Do **not** report IOA 300 (`M_ME_NA_1`, normalised): MSL5 v1 cannot map it. The IOA values above are the existing ones plus 303; they are only defaults — MSL5 rows must simply use the same CA/IOA/type.

### 7.2 When to report

1. Right after STARTDT confirmation (sends the full image once; this flips MSL5's state to `active`).
2. Every `-spont-interval` (default 5 s) while active, full image (`0` disables the timer).
3. Immediately (debounce ≤ 100 ms) after a command changed the image (`applySingle/Double/Analog/Bits`): report only the affected points, or simply the full image.
4. Nothing while not active.

Counter `M_IT`: increment `st.counter` by one on each full-image tick so the value visibly changes.

### 7.3 Building the frames (allowed causes per helper, verified in `asdu/mproc.go`)

| Frame | Helper | COT 3 allowed | COT 1 allowed |
|---|---|---|---|
| `M_SP_NA_1` | `asdu.Single(conn, false, coa, ca, info)` | yes | **no** |
| `M_DP_NA_1` | `asdu.Double(...)` | yes | **no** |
| `M_ME_NC_1` | `asdu.MeasuredValueFloat(...)` | yes | yes |
| `M_ME_NB_1` | `asdu.MeasuredValueScaled(...)` | yes | yes |
| `M_ME_TF_1` | existing `sendMeasuredFloatTime(conn, asdu.Spontaneous, ca, info)` (hand-built, keep it) or `asdu.MeasuredValueFloatCP56Time2a` | yes | no |
| `M_IT_NA_1` | existing `sendCounterRequest(conn, asdu.Spontaneous, ca, ioa, v)` or `asdu.IntegratedTotals` | yes | **no** (library restricts it) |

So the optional `-cyclic` mode (COT 1) applies **only to `M_ME_NC_1` and `M_ME_NB_1`**. Default is COT 3 everywhere. Use `asdu.CauseOfTransmission{Cause: asdu.Spontaneous}`.

### 7.4 Skeleton

```go
type reporter struct {
    st *station; conn asdu.Connect; ca asdu.CommonAddr
    interval time.Duration; cyclic bool
    mu sync.Mutex; stop chan struct{}      // nil = stopped
    kick chan struct{}                      // 1-buffered, written by apply*()
}

func (r *reporter) Start() { /* if already running return; make stop; go r.loop() */ }
func (r *reporter) Stop()  { /* close(stop) once; set nil */ }
func (r *reporter) Kick()  { select { case r.kick <- struct{}{}: default: } }   // called by apply*

func (r *reporter) loop() {
    r.reportAll()                                       // 7.2 (1)
    var tick <-chan time.Time
    if r.interval > 0 { t := time.NewTicker(r.interval); defer t.Stop(); tick = t.C }
    for { select {
        case <-r.stop: return
        case <-tick:   r.st.counter++ ; r.reportAll()
        case <-r.kick: time.Sleep(100*time.Millisecond); r.reportAll()   // debounce
    }}
}
```

`reportAll` snapshots the image under `st.mu`, releases the lock, then sends the six frames. **Never hold `st.mu` while calling `Send`.** Hand `conn` = the `*cs104.Client`.

## 8. Tests

### 8.1 Unit tests (no network), in `handler_test.go` style with `captureConn`

Keep the existing `captureConn`, `request`, `newTestStation`. Adapt/add:

1. `TestCommandsApplyToProcessImage` — for each of `C_SC/C_DC/C_SE_NA/NB/NC/C_BO`: image updated **and exactly two frames sent**: ActCon (COT 7) then ActTerm (COT 10), same type/CA/IOA/value, CA = request CA (use a CA ≠ `-ca` to prove the echo).
2. `TestOutOfScopeRequestsAreNotAnswered` — feed `C_IC_NA_1`, `C_CI_NA_1`, `C_RD_NA_1`, `C_CS_NA_1` into the corresponding handlers: `wantFrames(t, 0)`.
3. `TestReportAllFrames` — `reportAll` onto a `captureConn`: six frames, in the table's order; each decodes to the expected type, `COT == Spontaneous`, `CA == -ca`, IOA and value; `M_ME_TF_1` carries a time tag; no `M_ME_NA_1`.
4. `TestReportUsesImageValues` — change `spVal`/`ana`, report, assert the new values; the counter increments per tick.
5. `TestCyclicModeOnlyForMeasurements` — with `cyclic=true` the float/scaled frames are COT 1 and the others COT 3.
6. `TestReporterDoesNotSendWhenStopped` and `TestReporterStartStopIdempotent` (call Start/Stop repeatedly, race-free: run with `go test -race`).
7. `TestSendErrorsAreNotFatal` — a `captureConn` whose `Send` returns `cs104.ErrNotActive` / `ErrBufferFulled`: no panic, no busy loop.

### 8.2 Loopback test against a real server (recommended)

MSL5 contains a working reference of exactly this client: `src/packages/iec104/server_test.go` (`dialStation`, `sendSingleCmd`, `TestSpontaneousFrameBecomesEvent`, `TestCommandReachesStationAndConfirmationReturns`). Mirror the idea in VK: start `cs104.NewServer` with a minimal `ServerHandlerInterface` on `127.0.0.1:<free port>`, run the real VK client against it and assert: STARTDT handshake completes, the server's `ASDUHandler` receives the six spontaneous frames, a `C_SC_NA_1` sent with `asdu.SingleCmd(sess, …)` produces ActCon and ActTerm back, and killing the server makes the client reconnect when a new server appears. Note the library's server drops I-frames before STARTDT, so this also proves VK sends it.

## 9. Acceptance criteria

Functional (all must hold):

- [ ] VK dials `-msl5-host:-msl5-port`, sends STARTDT, receives STARTDT con; `SetServerActiveHandler` fires.
- [ ] Within 1 s of activation VK sends the full spontaneous image (6 frames, COT 3, CA = `-ca`); then every `-spont-interval`.
- [ ] A command changes the image, VK answers **ActCon then ActTerm** with the request's CA/IOA/type/value, and a spontaneous report with the new value follows within ~200 ms.
- [ ] VK never sends or answers GI, counter interrogation, `C_RD_*`, `C_CS`, `C_TS`, `C_RP`; the corresponding code is removed.
- [ ] VK never sends `M_ME_NA_1`, type IDs 58–64 or frames with COT other than 3 (or 1 in `-cyclic` for float/scaled).
- [ ] When MSL5 is stopped/restarted or the network drops, VK reconnects without restart and resumes reporting after the new STARTDT.
- [ ] `Start`/`Stop` of the reporter are idempotent; no goroutine leak after Ctrl+C (`-duration` exit is clean).
- [ ] `go build ./... && go vet ./... && go test -race ./...` pass; `gofmt -l .` is empty; `go.mod` has no `wendy512/iec104`.
- [ ] README, `docs/architecture.md` and the systemd unit describe the client role and the new flags.

End-to-end with MSL5 (§10) — all must be observable.

## 10. End-to-end procedure with MSL5

MSL5 side (any machine; replace `<api>` with MSL5's HTTP base URL; run MSL5 with PostgreSQL as usual):

1. Enable the server and bind all interfaces:
   `curl -X PUT <api>/api/iec104/server -H 'Content-Type: application/json' -d '{"enabled":true,"ipAddress":"0.0.0.0","port":2404}'`
   → response `state` is `listening`. (`error` + HTTP 400 means the port is taken.)
2. Create an **OUT** row that matches IOA 301 (301 = high 0, middle 1, low 45), CA 1 (high 0, low 1), with a linked analog-out channel `<channel-uuid>`; `asduTyp` is not needed for OUT:
   `curl -X POST <api>/api/iec104/batch -H 'Content-Type: application/json' -d '{"points":[{"type":"OUT","designation":"VK float","typ":"Wert","active":true,"asduHigh":0,"asduLow":1,"ioaHigh":0,"ioaMiddle":1,"ioaLow":45,"asduRetTyp":"M_ME_NC_1","ioaRetHigh":0,"ioaRetMiddle":1,"ioaRetLow":45,"channelId":"<channel-uuid>","channelName":"AO1"}]}'`
3. Create an **IN** row for the single command on IOA 100: `{"type":"IN","designation":"VK switch","typ":"Wert","active":true,"asduTyp":"C_SC_NA_1","asduHigh":0,"asduLow":1,"ioaHigh":0,"ioaMiddle":0,"ioaLow":100,"intervall":"5 sek"}` (the command value comes from the linked channel; without a channel it falls back to `true`).

VK side: `./iec104-server -msl5-host <msl5-ip> -msl5-port 2404 -ca 1 -verbose`.

Expected:

| Step | Observation |
|---|---|
| VK connects | MSL5 `GET /api/iec104/server` → `connected`, then `active` after VK's first frame; MSL5 log `station connected` / `station active`; VK log shows STARTDT confirmation |
| OUT | the OUT row's `responsePayload` updates every `-spont-interval` (`asduTyp: M_ME_NC_1`, `cot.cause: 3`, `value: 100`); on the Pi the SPI analog output is written (`spi.channel.write`); locally without SPI the MSL5 log shows `output write failed/not applied` — that is expected, the TCP/protocol path is still proven |
| IN | within the row interval the VK log prints `single command: IOA=100 …`; the IN row's `responsePayload` shows `C_SC_NA_1` with `cot.cause: 10` (ActTerm, after ActCon 7) |
| Loop | the command changes `spVal` (IOA 100); a spontaneous `M_SP_NA_1` follows immediately. Add an OUT row for `M_SP_NA_1`/IOA 100 (digital out) to see it |
| Robustness | stop MSL5 → VK logs reconnect attempts; start it → VK is `active` again within a few seconds; start a second VK → the first is dropped by MSL5 (newest wins) |

## 11. Pitfalls checklist

- Do **not** use `SendReplyMirror` (truncated bodies); build fresh ASDUs.
- `asdu.Single/Double/IntegratedTotals` reject COT 1; only float/scaled measurements accept it.
- `M_IT` via `asdu.IntegratedTotals` accepts COT 3 and counter-request causes only; the existing `sendCounterRequest` hand-builder accepts any cause.
- The cs104 client discards sends before STARTDT is confirmed (`ErrNotActive`): never send from `SetOnConnectHandler`, only from `SetServerActiveHandler` onward.
- Type IDs 58–64 cannot be decoded by go-iecp5 (missing size table); they are excluded on both sides. Do not add them.
- One VK instance only; a second one will be "newest wins" and the two will fight via auto-reconnect.
- `C_SE_NA_1` carries a raw int16 for MSL5 (engineering value). Do not divide by 32768.
- Library logging is off by default and swallows decode errors; use `-verbose` (`cli.LogMode(true)`) while testing.
- Findings A1–A12 of `IEC104_SERVER_ANALYSIS_AND_RECOMMENDATIONS.md` that concern GI/CI/RD/CS/SBO/negative confirmations are **out of scope** here; do not implement them.

## 12. Verification status of the MSL5 side

Verified in the MSL5 repository (Linux/WSL, no hardware): package tests with a real `cs104` client against the real listener (STARTDT, spontaneous frame → event, command → client, ActCon/ActTerm → event, GI/read ignored, newest-wins, reconnect, close/leak/race with `-race`), service tests with fakes, and one end-to-end test with a real TCP station through the service (spontaneous `M_ME_NC_1` → SPI write call; scheduled `C_SC_NA_1` → station; ActCon → stored). **Not verifiable off the device:** the actual SPI output of a 104-OUT write and the production deployment (vigil/nginx) — check those on the Pi. The VK repository was not modified when this document was written, and none of the VK snippets above were compiled there; they were checked against the go-iecp5 v1.2.5 source.
