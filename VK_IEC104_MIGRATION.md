# VK Client (IEC104_Go) — IEC 104 Migration Specification

| | |
|---|---|
| **Target repository** | `Mhd25OrCode/IEC104_Go`, folder `iec104_client/` |
| **Counterpart** | MSL5, branch `iec-104-new` (`src/packages/iec104`, `src/apps/backend/internal/device/iec104`) |
| **Protocol library** | `github.com/wendy512/go-iecp5 v1.2.5` (pinned in `iec104_client/go.mod`) |
| **Status** | Implemented in `iec104_client/` (reporter removed, `sender.go` and `station` monitoring image added) |
| **Related MSL5 docs** | `docs/adr/0005-iec104-msl5-als-server.md`, `docs/iec104-vk-implementation-handoff.md` (direction outdated), `docs/iec104-out-v1.md` (direction outdated) |

This document is an **implementation specification for the VK development team**. It describes
*what* must change, *where*, and *how the result will be verified*. It does not contain the finished
implementation.

---

## 0. Why this migration is required

### 0.1 Observed failure on the device

```text
Nov 25 14:22:21 mos-msl5 iec104-client[1709710]: [client] unhandled ASDU: type=TID<M_SP_NA_1>  COT=COT<UnknownTypeID> CA=1
Nov 25 14:22:21 mos-msl5 iec104-client[1709710]: [client] unhandled ASDU: type=TID<M_DP_NA_1>  COT=COT<UnknownTypeID> CA=1
Nov 25 14:22:21 mos-msl5 iec104-client[1709710]: [client] unhandled ASDU: type=TID<M_ME_NC_1> COT=COT<UnknownTypeID> CA=1
Nov 25 14:22:21 mos-msl5 iec104-client[1709710]: [client] unhandled ASDU: type=TID<M_ME_TF_1> COT=COT<UnknownTypeID> CA=1
Nov 25 14:22:21 mos-msl5 iec104-client[1709710]: [client] unhandled ASDU: type=TID<M_ME_NB_1> COT=COT<UnknownTypeID> CA=1
Nov 25 14:22:21 mos-msl5 iec104-client[1709710]: [client] unhandled ASDU: type=TID<M_IT_NA_1> COT=COT<UnknownTypeID> CA=1
... (identical block every 5 seconds)
```

### 0.2 Explanation

These six lines, repeated every 5 s, are **the VK client's own spontaneous report bouncing back**:

1. `reporter.go` (`reportAll()`) sends six `M_*` frames every `-spont-interval` (default 5 s) in the
   order `M_SP_NA_1` (IOA 100), `M_DP_NA_1` (200), `M_ME_NC_1` (301), `M_ME_TF_1` (302),
   `M_ME_NB_1` (303), `M_IT_NA_1` (400) — exactly the sequence in the journal.
2. MSL5's server hands every received ASDU to `srv_handler.go:53` (`ASDUHandler`), which accepts only
   `C_SC/C_DC/C_RC/C_SE_NA/C_SE_NB/C_SE_NC/C_BO` (`cmd.go:42`, `supportedOutCommands`) and returns an
   error for anything else — including all `M_*`.
3. `go-iecp5` reacts to a handler error by mirroring the frame back:
   `cs104/server_session.go:512-516` → `asduPack.SendReplyMirror(sf, asdu.UnknownTypeID)`.
4. COT 44 is printed as `UnknownTypeID` by the library stringer (`asdu/identifier.go`,
   `causeSemantics[44] = "UnknownTypeID"`); in IEC 60870-5-101/104 COT 44 means *unknown type
   identification* (negative confirmation).
5. The VK client routes every non-`C_IC/C_CI/C_RD/C_CS/C_TS/C_RP/C_CD` frame to `ASDUHandler`
   (`cs104/client.go:518`), and `station.OnASDU` has no `M_*` branch, so it hits the `default:` case in
   `handler.go` and prints `unhandled ASDU`.

### 0.3 Conclusion

The deployed VK client still implements the **superseded** contract (VK sends `M_*`, answers `C_*`).
MSL5 was inverted in commit `ea8a09c1` ("align IEC-104 direction semantics"): **MSL5 now sends the
monitoring frames and expects the VK client to send commands.** Both directions currently carry no
usable data: MSL5 rejects the client's `M_*` with COT 44, and the client does not consume MSL5's `M_*`.

---

## 1. Target communication contract

### 1.1 Direction

| Direction | Content | Owner of the value |
|---|---|---|
| **VK → MSL5** | Control commands `C_*` | VK (local inputs / process image) |
| **MSL5 → VK** | Monitoring/measurements `M_*` | MSL5 (its 104-IN rows, sent cyclically) |

```text
                        TCP connect (VK dials)                 VK = TCP client
  VK  ────────────────────────────────────────────────►  MSL5   MSL5 = TCP server
      ── STARTDT act ──►
      ◄─ STARTDT con ───
      ── C_* COT 6 (Activation) ──────────────────────►   [104-OUT]  MSL5 stores the command
      ◄─ C_* COT 7 (ActCon, mirrored by the library) ──              and writes the mapped channel
      ◄─ M_* COT 3 (cyclic monitoring) ───────────────   [104-IN]   VK stores value by CA+IOA
```

This is unusual on the application level (the controlling station reports monitoring and the
controlled station issues commands) but it is deliberate — do not "fix" the roles.

### 1.2 Wire parameters (must match MSL5)

| Item | Value |
|---|---|
| Transport | TCP, MSL5 port `2404` (configurable), VK dials `-msl5-host` |
| ASDU params | `asdu.ParamsWide`: COT 2 bytes (with originator address), CA 2 bytes, IOA 3 bytes |
| Timers | `cs104.DefaultConfig()` (t0 30 s, t1 15 s, t2 10 s, t3 20 s, k 12, w 8) — keep |
| STARTDT | **VK must send it** (`SendStartDt()` in `SetOnConnectHandler`); MSL5 never sends STARTDT |
| CA | VK `-ca` (default 1) for outgoing commands; incoming `M_*` are accepted for **any** CA and keyed by it |
| IOA | 3 bytes, never 0 |
| Sessions | MSL5 keeps exactly one session, newest wins → run **one** VK instance per MSL5 |
| Keep-alive | MSL5 sends `TESTFR act`; `cs104.Client` answers automatically |

### 1.3 Incoming monitoring types (MSL5 → VK)

MSL5 sends every active 104-IN row as a single-object ASDU (`SQ=0`), always with
**COT 3 (`Spontaneous`)** — the cyclicity is produced by MSL5's scheduler, not by the COT value
(`src/packages/iec104/commander.go:16-17`).

| Type | TypeID | Decoder in `asdu` | Value type |
|---|---|---|---|
| `M_SP_NA_1` | 1 | `pack.GetSinglePoint()` | `SinglePointInfo.Value bool`, `Qds`, `Time` |
| `M_DP_NA_1` | 3 | `pack.GetDoublePoint()` | `DoublePointInfo.Value DoublePoint (0..3)`, `Qds` |
| `M_ME_NC_1` | 13 | `pack.GetMeasuredValueFloat()` | `MeasuredValueFloatInfo.Value float32`, `Qds` |
| `M_ME_TF_1` | 36 | `pack.GetMeasuredValueFloat()` | same, `Time` set from CP56Time2a |
| `M_ME_NB_1` | 11 | `pack.GetMeasuredValueScaled()` | `MeasuredValueScaledInfo.Value int16`, `Qds` |
| `M_IT_NA_1` | 15 | `pack.GetIntegratedTotals()` | `BinaryCounterReadingInfo.Value.CounterReading int32` |

All `Get*` helpers return a **slice** and already handle `SQ=1` and multiple information objects
(`asdu/mproc.go`, e.g. `GetSinglePoint` at 1131, `GetMeasuredValueFloat` at 1340) — iterate the slice,
do not assume a single element.

### 1.4 Outgoing command types (VK → MSL5)

The list is fixed by MSL5 (`src/packages/iec104/cmd.go:42-50`, `supportedOutCommands`). Any other
type is rejected by MSL5.

| Type | TypeID | MSL5 decoder | Value on the wire | Builder in `asdu` |
|---|---|---|---|---|
| `C_SC_NA_1` | 45 | `GetSingleCmd()` | `bool` + QOC | `asdu.SingleCmd` |
| `C_DC_NA_1` | 46 | `GetDoubleCmd()` | `uint8` 0..3 + QOC | `asdu.DoubleCmd` |
| `C_RC_NA_1` | 47 | `GetStepCmd()` | `uint8` 0..3 + QOC | `asdu.StepCmd` |
| `C_SE_NA_1` | 48 | `GetSetpointNormalCmd()` | `int16` **raw** (no /32768) + QOS | `asdu.SetpointCmdNormal` |
| `C_SE_NB_1` | 49 | `GetSetpointCmdScaled()` | `int16` + QOS | `asdu.SetpointCmdScaled` |
| `C_SE_NC_1` | 50 | `GetSetpointFloatCmd()` | `float32` + QOS | `asdu.SetpointCmdFloat` |
| `C_BO_NA_1` | 51 | `GetBitsString32Cmd()` | `uint32` | `asdu.BitsString32Cmd` |

All seven builders accept **COT 6 (`asdu.Activation`)** and reject everything else
(`asdu/cproc.go:37, 95, 148, 201, 253, 305, 359`), which matches MSL5's requirement exactly.

### 1.5 Explicitly out of scope

- General interrogation (`C_IC_NA_1`), counter interrogation (`C_CI_NA_1`), read (`C_RD_NA_1`),
  clock sync (`C_CS_NA_1`), test (`C_TS_NA_1`), reset (`C_RP_NA_1`), delay acquisition (`C_CD_NA_1`).
  `client_handler.go` already returns `nil` for all of them — **keep that**, never answer.
- Time-tagged commands 58–64 (`C_SC_TA_1` … `C_BO_TA_1`): `go-iecp5` has no `infoObjSize` entry for
  them, frames cannot be decoded; MSL5 refuses to configure them.
- Normalised measurements (`M_ME_NA_1`, `M_ME_TA_1`, `M_ME_TD_1`, `M_ME_ND_1`): MSL5 does not send them.
- No general interrogation, no read, no answers to any request.

---

## 2. Remove the old reporter (remove VK → `M_*`)

### 2.1 Files

| File | Required change |
|---|---|
| `iec104_client/reporter.go` | Delete the file (or reduce it to the command sender from §4). `reporter`, `newReporter`, `Start`, `Stop`, `Kick`, `loop`, `reportAll`, `clampInt16` all become obsolete. |
| `iec104_client/main.go` | In `newClient()` remove `rep := newReporter(...)`, `st.onChange = rep.Kick`, `SetServerActiveHandler`→`rep.Start()`, `SetConnectionLostHandler`→`rep.Stop()`, and the `rep.Stop()` handling in `main()`. Remove the now unused flags `-spont-interval` and `-cyclic`. |
| `iec104_client/confirm.go` | `sendMeasuredFloatTime()` and `sendCounterRequest()` exist only for the report and must be removed with it. Keep `newCommandASDU()` — it is reused by §4. |
| `iec104_client/handler.go` | The `station.onChange` field and the `changed()` callback must no longer trigger a send. Keep `apply*()` only if the local process image is still needed as a command source; `changed()` must only update local state / log at verbose level. |
| `iec104_client/client_handler.go` | Keep `ignore()` and the eight handlers; drop any reporter-only plumbing. |

### 2.2 Expected behaviour afterwards

- The VK client emits **no** `M_*` frames at all — neither on connect, nor periodically, nor on change.
- MSL5 no longer produces COT 44 (`UnknownTypeID`) mirrors, therefore the
  `unhandled ASDU: … COT<UnknownTypeID>` lines disappear from `journalctl -u iec104-client`.
- The only I-frames VK sends are `C_*` (see §4) and library-level frames (STARTDT, TESTFR).

### 2.3 Acceptance

- `grep -rn "sendMeasuredFloatTime\|sendCounterRequest\|reportAll" iec104_client/` finds nothing.
- A 60 s passive session (`-cmd-interval 0`) shows **zero** outgoing I-frames in `-verbose` mode.

---

## 3. Handle incoming `M_*` messages (MSL5 → VK)

### 3.1 Where

`iec104_client/handler.go` → `(*station).OnASDU()` (reached through `client_handler.go` → `ASDUHandler`).
`cs104/client.go:518` routes every type that is not one of the seven dedicated handler cases to
`ASDUHandler`, so all `M_*` frames arrive here.

### 3.2 Required behaviour

1. Add a `switch` over the six types from §1.3, decode with the matching `Get*` helper, and iterate the
   returned slice (handle `SQ=1` and multiple information objects).
2. Store each decoded object in a **monitoring image** keyed by `(CommonAddr, InfoObjAddr)`:

   ```go
   type pointKey struct {
       CA  asdu.CommonAddr
       IOA asdu.InfoObjAddr
   }

   type monitoringValue struct {
       Type  asdu.TypeID
       Value any       // bool | uint8 | float32 | int16 | int32
       Qds   asdu.QualityDescriptor
       Time  time.Time // zero for the _NA_1 variants
       At    time.Time // receive time
   }
   ```

   Store it under a mutex; expose a read accessor (e.g. `Snapshot() map[pointKey]monitoringValue`).
3. The CA comes from the frame (`pack.CommonAddr`), the IOA from the information object — never from a
   constant, because MSL5 rows carry their own CA/IOA.
4. Unknown/unsupported `M_*` types (e.g. `M_ME_NA_1`): log at **debug/verbose** level only and return
   `nil`. Never return an error: client handler errors are only logged (`cs104/client.go:437-439`), but
   `nil` keeps the intent explicit and avoids noise.
5. Log the applied value only when `-verbose` is set (see §7).
6. **No feedback loop:** incoming `M_*` must never trigger an outgoing `C_*`. The monitoring image is
   read-only data for display/consumption; the command source (§4) is a separate image. Otherwise
   MSL5's echo of a command would re-arm the command sender and produce a permanent loop.

> **Update (implemented).** The sender now *reads* the monitoring image while it
> builds a round (`station.CommandValue` / `sender.commandValue`), so a point
> with a compatible `M_*` for the same CA + IOA carries the value MSL5 last
> reported (`M_SP_NA_1` → `C_SC_NA_1`, `M_DP_NA_1` → `C_DC_NA_1`,
> `M_ME_NB_1` → `C_SE_NB_1`, `M_ME_NC_1`/`M_ME_TF_1` → `C_SE_NC_1`). The rule
> above still holds: a received frame never triggers a send.

### 3.3 Expected behaviour

- After the first `M_*` burst, `Snapshot()` contains entries for CA 1 / IOA 100, 200, 301, 302, 303, 400
  (assuming MSL5 rows with those addresses).
- Values update on every incoming frame; quality (`Qds`) is preserved; `M_ME_TF_1` keeps its CP56Time2a.
- No client log line is produced for these frames at default verbosity.

### 3.4 Acceptance

- Unit test `TestIncomingMonitoringUpdatesImage` (§8.3).
- With `-verbose`, received frames are decoded and logged, and no `unhandled ASDU` line appears for the
  six supported types.

---

## 4. Implement outgoing `C_*` commands (VK → MSL5)

### 4.1 New sender component

Replace `reporter.go` with a sender (e.g. `iec104_client/sender.go`):

```go
// Build one command per configured point, build the ASDU fresh (§6),
// send with COT Activation, and honour the library's error semantics.
type sender struct {
    conn     asdu.Connect
    ca       asdu.CommonAddr
    points   []commandPoint   // CA/IOA/type/value source
    interval time.Duration    // 0 = no periodic send
    mu       sync.Mutex
    stop     chan struct{}
    done     chan struct{}
    kick     chan struct{}    // 1-buffered, send-on-change
}
```

- `Start()` / `Stop()` must be idempotent and goroutine-safe (fired from
  `SetServerActiveHandler` / `SetConnectionLostHandler`, which can fire on every reconnect).
- `Kick()` is called locally when a command value changes (debounce ≈ 100 ms).
- Periodic mode: every `-cmd-interval` re-send the current commands.
- Send **only while the session is active** (`SetServerActiveHandler` fired). Before that
  `cs104.Client.Send` returns `ErrNotActive`.

### 4.2 Flags

| Flag | Default | Meaning |
|---|---|---|
| `-cmd-interval` | `10s` | period of the command cycle; `0` = only send-on-change |
| `-cmd-on-change` | `true` | send immediately (debounced) when a command value changes |
| `-ca` | `1` | CA of all outgoing commands |

Remove `-spont-interval` and `-cyclic`. Update `deploy/iec104-client.service` and `install.sh` if they
pass those flags.

### 4.3 Building and sending

- One information object per ASDU (`SQ=0`, `Variable.Number = 1`).
- COT is **always** `asdu.CauseOfTransmission{Cause: asdu.Activation}` (COT 6). MSL5 silently drops any
  other cause without a reply (`srv_handler.go:59-63`).
- Use the library builders from §1.4. If a value kind cannot be represented (e.g. `C_DC_NA_1` with a
  value > 3), clamp to the legal range and log at verbose level instead of sending garbage.
- `C_SE_NA_1` carries the **raw int16** engineering value — do not normalise by 1/32768.
- MSL5 stores the command in the OUT row's `responsePayload` and writes it to the mapped SPI channel.

### 4.4 Error handling

| Error | Reaction |
|---|---|
| `cs104.ErrNotActive` | Drop the frame silently, wait for the next tick / next activation |
| `cs104.ErrUseClosedConnection` | Drop silently, wait for reconnect |
| `cs104.ErrBufferFulled` | Log a warning (rate-limited), skip this round; never busy-retry |
| other | Log once, continue with the next command |

No retries inside a tick, no unbounded loops, no blocking of the library handler goroutine.

### 4.5 Acceptance

- Unit test `TestOutgoingCommandsUseActivation` (§8.3): every captured frame has
  `COT.Cause == asdu.Activation`, the configured CA/IOA, the expected type and value.
- Integration test: MSL5-like server receives exactly one command per configured point per tick.
- No `M_*` is ever sent.

---

## 5. Handle command confirmations (COT 7 / COT 10)

### 5.1 What MSL5 sends back

- MSL5 confirms **accepted** commands by mirroring the received ASDU with `ActivationCon` (COT 7):
  `src/packages/iec104/srv_handler.go:64-68`, `p.SendReplyMirror(c, asdu.ActivationCon)`.
- MSL5 confirms nothing for rejected frames: unknown type → COT 44 mirror (only possible if VK sends a
  type outside §1.4); wrong COT, invalid CA/IOA → dropped silently.
- MSL5 never sends `ActivationTerm` (COT 10) in this role, but a peer may. Both must be handled.

### 5.2 Required behaviour in `(*station).OnASDU()`

1. **Classify by cause first, type second.** Only `pack.Coa.Cause == asdu.Activation` (6) is a command to
   execute. Everything else — `ActivationCon` (7), `ActivationTerm` (10), `Deactivation` (8),
   `DeactivationCon` (9), negative/test bits — is *not* executed.
2. For COT 7 / COT 10: record the confirmation (e.g. `lastConfirmation[pointKey]{cause, at}`), log at
   verbose level, return `nil`. **Do not** apply the value to the process image and **do not** send any
   reply.
3. Remove the current behaviour of answering every received command with `ActCon` **and** `ActTerm`
   (`confirmTwice(...)` in `handler.go`). In the new contract VK is the *controlling* station: MSL5
   generates the confirmation, VK must not.
4. Keep the existing `if pack.Identifier.Coa.Cause == asdu.Deactivation { … return nil }` guard and
   extend it to the "not Activation" rule.

### 5.3 Why this is mandatory

Without the cause check the client would treat MSL5's COT 7 echo as a new command: it would apply the
value a second time and emit two more `C_*` frames, which MSL5 would again confirm with COT 7 — a
self-sustaining loop (and duplicate writes on MSL5).

### 5.4 Acceptance

- Unit test `TestConfirmationIsNotExecuted`: feed a `C_SC_NA_1` with COT 7 and COT 10 into `OnASDU`;
  the process image is unchanged and `captureConn` records **0** frames.
- Integration test `TestNoCommandLoop`: send one command; assert the client emits exactly one frame and
  no further frames for at least 3 intervals.

---

## 6. ASDU construction rules

1. **Build fresh ASDUs** for every outgoing frame — either with the library builders from §1.4 or with
   the existing hand-built helper `newCommandASDU()` from `confirm.go`. Both are safe.
2. **Never** call `ASDU.SendReplyMirror()` after a `Get*Cmd()` call. The getters consume the internal
   information-object buffer, so a mirror built afterwards is truncated
   (see `docs/iec104_library_bug_report.md` §1 in the VK repo). This applies even if the mirror is
   created by the library for a returned handler error.
3. Command builders validate type ID and cause; a mismatch yields `asdu.ErrTypeIDNotMatch` — map it to a
   verbose log, do not panic.
4. Quality/select bits: use the builder's `Qoc`/`Qos` fields (`InSelect = false` for direct execute,
   `Qual` as required by the point configuration). MSL5 stores what it receives; it does not validate
   `select`.
5. Do not send type IDs 58–64 (`infoObjSize` missing in v1.2.5, `docs/iec104_library_bug_report.md` §2).
6. Do not send `C_IC_NA_1`, `C_CI_NA_1`, `C_RD_NA_1`, `C_CS_NA_1`, `C_TS_NA_1`, `C_RP_NA_1`,
   `C_CD_NA_1`.

### 6.1 Acceptance

- Code review: no `SendReplyMirror` in any sending path.
- `go vet ./...` clean; no `panic` reachable from ASDU construction.

---

## 7. Logging

### 7.1 Required changes

| Location | Today | Required |
|---|---|---|
| `handler.go` `OnASDU` `default:` | `fmt.Printf("[client] unhandled ASDU: …")` **always** | Only when `-verbose`; otherwise silent |
| `handler.go` command cases | `fmt.Printf("[client] single command: …")` etc. **always** | `-verbose` only |
| `handler.go` `changed()` | `fmt.Printf("[client] applied: …")` **always** | `-verbose` only |
| `client_handler.go` `ignore()` | already `-verbose`-gated | keep |
| `main.go` flags | only `-verbose` (per-frame lines + library log) | add `-log-frames` for the app-level `TX`/`RX` lines only; keep `-verbose` = `-log-frames` + library log |
| `reporter.go` `logSendError()` | unconditional (file is removed) | move the equivalent into the sender, keep the error-class filtering |
| incoming `M_*` application | new | `-log-frames` only |

### 7.2 Default output

With no flags the unit log contains **only** lifecycle events:

```text
[client] connected to 127.0.0.1:2404, sending STARTDT
[client] STARTDT confirmed, session active
[client] connection lost
[client] command send failed: …        (unexpected errors only)
```

No per-frame lines. The shipped unit passes `-log-frames`, so journald also gets
one `TX`/`RX` line per ASDU (at the default `-cmd-interval` that is ~7 sent
commands plus ~7 received `ActCon` lines per 10 s; drop the flag or use
`-cmd-interval 0` if that is too noisy on the Pi). `-verbose` additionally turns
on the library's raw APDU log.

### 7.3 Acceptance

- With `-log-frames` removed, `journalctl -u iec104-client` over 10 minutes
  contains no per-frame lines (`TID<…>`, `COT<…>`, `unhandled ASDU`).
- `iec104-client -log-frames` shows incoming `M_*` (`RX … value=… qds=…`),
  outgoing `C_*` (`TX … COT<Activation>`), confirmations and ignored types.
- `iec104-client -verbose` implies `-log-frames` and adds the raw APDU dump.

---

## 8. Testing requirements

### 8.1 Unit tests (`handler_test.go` style, `captureConn`, no network)

Existing `captureConn`, `request`, `newTestStation` helpers stay and are adapted.

### 8.2 Integration tests with `cs104.NewServer`

Use a real `cs104` server with an **MSL5-like handler** instead of a hand-rolled fake:

```go
// MSL5-like server behaviour: accept the seven command types, mirror ActCon (COT 7),
// send M_* frames with COT 3, ignore everything else.
srv := cs104.NewServer(&msl5LikeHandler{frames: capture})
srv.SetConfig(cs104.DefaultConfig())
srv.SetParams(asdu.ParamsWide)
srv.SetLogProvider(discardLogger)
srv.LogMode(false)
go srv.ListenAndServer(addr)     // addr = 127.0.0.1:<free port>
// ... run the real cs104.Client against it (with AutoReconnect, like production)
```

The handler implements `cs104.ServerHandlerInterface` (8 methods); `ASDUHandler` must
(a) reject any type outside §1.4 with an error, so the test reproduces MSL5's COT 44 behaviour,
(b) return `nil` for non-Activation causes (MSL5 does the same), and
(c) mirror accepted commands with `p.SendReplyMirror(c, asdu.ActivationCon)`.

Reference implementation on the MSL5 side: `src/packages/iec104/server_test.go`
(`dialStation`, `sendSingleCmd`, `TestSpontaneousFrameBecomesEvent`,
`TestCommandReachesStationAndConfirmationReturns`).

### 8.3 Required test cases

| # | Test | Setup | Assertion |
|---|---|---|---|
| 1 | `TestIncomingMonitoringUpdatesImage` | server sends the six `M_*` from §1.3 with CA 1 / IOA 100/200/301/302/303/400 | image contains all six with correct values, `M_ME_TF_1` has a non-zero `Time`, `Qds` preserved |
| 2 | `TestIncomingMonitoringUsesFrameCA` | same frames with CA 7 | stored under CA 7, not 1 |
| 3 | `TestIncomingMonitoringMultipleInfoObjects` | one `M_ME_NC_1` with `SQ=1`, 3 objects | three entries |
| 4 | `TestNoMonitoringFramesEmitted` | passive client, `-cmd-interval 0` | `captureConn` records 0 I-frames over ≥ 2 s |
| 5 | `TestOutgoingCommandsUseActivation` | one point per type from §1.4 | each frame: `COT.Cause == asdu.Activation`, type/CA/IOA/value correct; exactly one frame per tick |
| 6 | `TestSendOnChange` | change a value, debounce 100 ms | exactly one additional frame, value updated |
| 7 | `TestConfirmationIsNotExecuted` | feed `C_SC_NA_1` with COT 7 and COT 10 | image unchanged, 0 frames sent, confirmation recorded |
| 8 | `TestNoCommandLoop` | integration: client sends 1 command, server mirrors ActCon | over 3 intervals the server receives exactly 1 command |
| 9 | `TestRejectedTypeGetsNoReply` | server sends an unsupported type | client sends nothing, logs only at frame level |
| 10 | `TestSenderIdempotent` | `Start`/`Stop` repeatedly, `-race` | no panic, no goroutine leak |
| 11 | `TestSendErrorsAreNotFatal` | `captureConn` returning `ErrNotActive` / `ErrBufferFulled` | no panic, no busy loop |
| 12 | `TestFrameLogGating` | an unhandled `OnASDU` type and one sent command, first at default verbosity, then with `-log-frames` | no output by default; `RX`/`TX` lines with the flag |
| 13 | `TestReconnectResumesSending` | kill server, restart | commands resume after new STARTDT without restarting the client |

### 8.4 Quality gate

```bash
go build ./... && go vet ./... && go test -race ./... && test -z "$(gofmt -l .)"
```

### 8.5 End-to-end with real MSL5

1. MSL5: `PUT /api/iec104/server` with `{"enabled":true,"ipAddress":"0.0.0.0","port":2404}`.
2. MSL5: create at least one **IN** row (MSL5 → `M_*`) and one **OUT** row (VK → `C_*`) with matching
   CA/IOA/type and a linked channel.
3. Run `./iec104-client -msl5-host <ip> -msl5-port 2404 -ca 1 -cmd-interval 10s -log-frames`.
4. Expected:
   - `GET /api/iec104/server` → `active`;
   - client log shows the incoming `M_*` values of the MSL5 IN row;
   - MSL5 OUT row `responsePayload` shows the `C_*` frame with `cot.cause: 7`;
   - the commanded channel value changes in MSL5;
   - no COT 44 / `unhandled ASDU` lines anywhere.

---

## 9. Documentation that must be updated

### 9.1 VK repository (`Mhd25OrCode/IEC104_Go`)

| File | Section / content to change |
|---|---|
| `README.md` | Header (client role: sends `C_*`, receives `M_*`), flag table (remove `-spont-interval`/`-cyclic`, add `-cmd-interval`/`-cmd-on-change`), run examples, the "No unsolicited traffic" section, description of the process image |
| `docs/architecture.md` | §1 overview, §3 data flow (direction of `M_*`/`C_*`), §11 MSL5 contract; remove the spontaneous-report description |
| `docs/iec104_library_bug_report.md` | Add a note that `SendReplyMirror` remains forbidden and that the COT 44 mirror is produced by the *server* library when the application handler returns an error |
| `deploy/iec104-client.service` | `ExecStart` flags if changed; keep `Restart=always`, `RestartSec=2` |
| `deploy/install.sh`, `build.sh` | Only if flags/units are referenced |
| Superseded plan copies in `docs/` | Mark as superseded or delete (they describe the old direction) |

### 9.2 MSL5 repository (counterpart action, not VK scope)

| File | Issue |
|---|---|
| `docs/iec104-vk-implementation-handoff.md` | Describes VK → `M_*` / MSL5 → `C_*` (reversed); rewrite or mark obsolete |
| `docs/iec104-out-v1.md` | Same reversed direction |
| `docs/adr/0005-iec104-msl5-als-server.md` | §1/§4 still describe the old 104-OUT/104-IN semantics |

---

## 10. Acceptance criteria — Definition of Done

Functional:

- [ ] VK sends **no** `M_*` frames, ever (no reporter, no `-spont-interval`, no `-cyclic`).
- [ ] VK sends `C_*` frames only for the seven types in §1.4, always with COT 6, correct CA/IOA/value.
- [ ] VK decodes the six `M_*` types from §1.3 and stores them keyed by frame CA + IOA, including
      `SQ=1`/multiple information objects, quality and CP56Time2a.
- [ ] COT 7 / COT 10 are recognised as confirmations, never executed, never answered.
- [ ] No command loop: one command produces exactly one outgoing frame.
- [ ] No `SendReplyMirror` on any outgoing path; all ASDUs are built fresh.
- [ ] `unhandled ASDU` and per-frame logs are only emitted with `-log-frames` (`-verbose` also enables them).
- [ ] Reconnect: after MSL5 restarts, commands resume automatically after the new STARTDT.
- [ ] One VK instance only; the unit does not start a second one.

Quality gate:

- [ ] `go build ./...`, `go vet ./...`, `go test -race ./...` pass; `gofmt -l .` is empty.
- [ ] Tests §8.3 (1–13) exist and pass, including at least two `cs104.NewServer` integration tests.
- [ ] Documentation §9.1 updated; no stale references to spontaneous reporting.

Operational:

- [ ] On the device: `journalctl -u iec104-client` shows no `COT<UnknownTypeID>` and no `unhandled ASDU`
      at default verbosity over 10 minutes.
- [ ] MSL5 `GET /api/iec104` shows incoming OUT-row confirmations with `cot.cause: 7` and updated channel
      values; the IN row values appear in the VK client log with `-log-frames`.

---

## 11. Pitfalls checklist

- **Do not** re-add spontaneous reporting "for compatibility": MSL5 answers it with COT 44 and it is
  decoded nowhere.
- **Do not** execute `C_*` frames with COT ≠ 6. MSL5 mirrors every accepted command as COT 7 — executing
  it creates an infinite command/confirmation loop.
- **Do not** send `ActCon`/`ActTerm` back to MSL5: in this contract MSL5 is the confirming side
  (`srv_handler.go` accepts COT 7/10 without answering; the frames are still useless traffic).
- **Do not** call `SendReplyMirror()` after `Get*Cmd()` — truncated body.
- **Do not** send type IDs 58–64: `go-iecp5` v1.2.5/v1.2.6 cannot encode/decode them.
- **Do not** normalise `C_SE_NA_1`: MSL5 expects the raw int16 engineering value.
- **Do not** send before STARTDT is confirmed (`ErrNotActive`); start the sender in
  `SetServerActiveHandler`, stop it in `SetConnectionLostHandler`.
- **Do not** hold a mutex while calling `conn.Send`.
- **Do not** run two VK instances against one MSL5 (newest wins; the two fight via auto-reconnect).

---

## Appendix A — MSL5 reference map (why the contract looks like this)

| MSL5 location | Behaviour |
|---|---|
| `src/packages/iec104/server.go` | `cs104.NewServer`, one session (newest wins), `ParamsWide`, event channel |
| `src/packages/iec104/srv_handler.go:53` | Incoming `C_*` only; `ActCon` mirror; error → COT 44 |
| `src/packages/iec104/srv_handler.go:59` | Incoming command with COT ≠ 6 is ignored without reply |
| `src/packages/iec104/cmd.go:42` | `supportedOutCommands` — the seven accepted command types |
| `src/packages/iec104/cmd.go:29` | `valueKinds` — the monitoring types MSL5 can send |
| `src/packages/iec104/commander.go:16` | Outgoing `M_*` always COT 3 (`Spontaneous`) |
| `src/apps/backend/internal/device/iec104/service/dispatch.go:20` | Active IN rows are sent as `M_*` |
| `src/apps/backend/internal/device/iec104/service/scheduled.go` | Cyclic dispatch + logging of the sent value |
| `cs104/server_session.go:512-516` (library) | `ASDUHandler` error → `SendReplyMirror(UnknownTypeID)` (COT 44) |
| `asdu/identifier.go` `causeSemantics[44]` (library) | Prints as `UnknownTypeID` |
| `cs104/client.go:518` (library) | Every non-dedicated type → `ASDUHandler` |
| `asdu/cproc.go:37…359` (library) | Command builders accept COT 6/8 only |
| `asdu/mproc.go:1131/1164/1340/1374` (library) | `M_*` getters return slices, `SQ`-aware |

## Appendix B — Symptom → cause table for the current logs

| Log line | Cause |
|---|---|
| `[client] unhandled ASDU: type=TID<M_*>` | VK receives an `M_*` frame; `OnASDU` has no `M_*` branch |
| `COT=COT<UnknownTypeID>` | COT 44 negative confirmation, mirrored by the MSL5 server library because `srv_handler` rejected the frame |
| Six lines every 5 s in fixed order | VK's own `reportAll()` cycle (`-spont-interval` default 5 s) |
| No `COT<Spontaneous>` lines | MSL5 currently sends no `M_*` (no active IN row with a resolvable value, or scheduler idle) |
