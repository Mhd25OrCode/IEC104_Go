# IEC 60870-5-104 – Test Outstation – Required Changes

**Status: implemented (2026-11-18).** See §11 "Implementation status" for what was
changed, where, and how it was verified. The analysis below is kept as the
original plan; §1 deployment facts still hold except that the repository no
longer ships a master client (`dist/iec104-client` is gone).

> **Status history:** *planning only – no code was modified* (2026-11-16) →
> *implemented* (2026-11-18, uncommitted working-copy changes:
> `iec104_server/main.go`, `handler.go`, `confirm.go`, new `handler_test.go`).

- Target repository: `https://github.com/Mhd25OrCode/IEC104_Go`, folder `iec104_server`
  (the Go outstation, deployed on the device as the systemd unit `iec104-server`).
- Counterpart: the MSL5 master on the same machine
  (`src/apps/backend/internal/device/iec104`, branch `iec-104-new`).
- Motivation: with the new „104-IN“ semantics MSL5 sends **one `C_*` command per
  active IN row** (`C_RD`, `C_SC`, `C_DC`, `C_SE_*`, `C_BO`) and stores the frames the
  outstation answers with. The outstation must therefore (a) keep the process image
  consistent and (b) answer each address with the correct ASDU type.

---

> Verified 2026-11-16 against `main` (`c74ea9e`, 2026-10-09) of the repo, folder
> `iec104_server` (`main.go`, `handler.go`, `confirm.go`; `go-iecp5 v1.2.5`,
> `wendy512/iec104 v1.0.4`). §2 matches the code. Library fact: the server session only
> rejects `CommonAddr == 0` (`InvalidCommonAddr`); it does **not** check the CA against `-ca`,
> so requests with any CA reach the handlers.

## 1. Deployment facts (verified in the repo)

| Item | Value |
|---|---|
| Binary build | `build.sh` → `CGO_ENABLED=0 GOOS=linux GOARCH=arm64`, output `dist/iec104-server` (the `dist/iec104-client` output was removed on 2026-11-18 with the master client) |
| Install | `deploy/install.sh` (as root): user `iec104`, prefix `/opt/iec104`, unit copied to `/etc/systemd/system/iec104-server.service`, `systemctl enable --now iec104-server.service` |
| Unit | `ExecStart=/opt/iec104/iec104-server -host 0.0.0.0 -port 2404`, `Restart=always`, `RestartSec=2` |
| Logs | `journalctl -u iec104-server -f` (stdout of the process; `-verbose` enables `go-iecp5` frame logging) |
| Library | `github.com/wendy512/iec104` + `github.com/wendy512/go-iecp5` (Bug 3: Type IDs 58–64 are not decodable) |

---

## 2. Current behaviour (verified in `iec104_server/*.go`)

### 2.1 Process image (`handler.go`, `station` struct)

```go
spVal bool; dpVal asdu.DoublePoint; ana float32; counter int32; bits uint32
```

- `periodicLoop` (default `-period 3s`): increments `ana`, reports `M_ME_NC_1` at
  **IOA 301**, `COT = Periodic(1)`.
- `spontaneousLoop` (default `-spontaneous 2s`): toggles `spVal`, reports
  `M_SP_TB_1` at **IOA 100** and `M_ME_TF_1` at **IOA 302**, `COT = Spontaneous(3)`.
- Both loops are started **only** from `OnInterrogation` (`startReporting`), i.e. there is
  no background traffic before a general interrogation.
- `sendAllData` (GI answer, `COT = InterrogatedByStation(20)`):
  `M_SP_NA_1`@100, `M_DP_NA_1`@200, `M_ME_NA_1`@300, `M_ME_NC_1`@301, `M_ME_TF_1`@302.
- `sendCounters` (counter interrogation, `COT = RequestByGeneralCounter(37)`):
  `M_IT_NA_1`@400.

### 2.2 Reads (`handler.go`, `OnRead`)

```go
func (st *station) OnRead(conn asdu.Connect, _ *asdu.ASDU, addr asdu.InfoObjAddr) error {
    st.mu.Lock(); v := st.spVal; st.mu.Unlock()
    return asdu.Single(conn, false, coa(asdu.Request), st.ca,
        asdu.SinglePointInfo{Ioa: addr, Value: v, Qds: asdu.QDSGood})
}
```

⇒ **every** IOA is answered with an `M_SP_NA_1` carrying `spVal`, regardless of what the
object at that IOA really is (IOA 301 = float, 400 = counter, …).

### 2.3 Commands (`handler.go`, `OnASDU`)

| Received | Action |
|---|---|
| `C_SC_NA_1`/`C_SC_TA_1` | print + `confirmSingleCmd` (echo, `ActivationCon`) – **no process-image update** |
| `C_DC_NA_1`/`C_DC_TA_1` | print + `confirmDoubleCmd` – **no update** |
| `C_SE_NA_1`…`C_SE_TC_1` | print + `confirmSetpoint*` – **no update** |
| `C_BO_NA_1`/`C_BO_TA_1` | print + `st.bits = bc.Value` + `confirmBitsString32Cmd` (only updated field) |
| `C_IC_NA_1` | `OnInterrogation` (outside `OnASDU`) |
| `C_CI_NA_1` | `OnCounterInterrogation` |
| `C_RD_NA_1` | `OnRead` (see 2.2) |
| `C_CS_NA_1`, `C_TS_NA_1`, `C_TS_TA_1`, `C_RP_NA_1` | confirmation only (IOA = `InfoObjAddrIrrelevant`) |

`confirm.go` always builds a **fresh** ASDU (`newCommandASDU`) with `ActivationCon(7)` –
this is correct and must stay (mirroring is unsafe after `Get*Cmd()` consumed the buffer).

### 2.4 Consequences for MSL5

1. Sending `C_SC`/`C_DC`/`C_SE` changes nothing ⇒ a later read returns the old value.
2. A read of IOA 301/400 returns an `M_SP_NA_1` (bool), so MSL5 shows a single point
   where the user expects a measured value/counter.
3. Types 58–64 (`C_SE_TA_1`/58, `C_SE_TB_1`/59, `C_SE_TC_1`/60, `C_BO_TA_1`/64) cannot be
   decoded by the library on **either** side ⇒ MSL5 must not use them (already excluded).

---

## 3. Required changes

### O1 – Apply received commands to the process image (`handler.go`, `OnASDU`)

Today only `C_BO_NA_1` stores its value. Every supported command must update the
process image **before** the confirmation is sent:

| Command | Field | Mapping |
|---|---|---|
| `C_SC_NA_1` | `spVal` | `st.spVal = sc.Value` |
| `C_DC_NA_1` | `dpVal` | `st.dpVal = asdu.DoublePoint(dc.Value & 0x03)` |
| `C_SE_NA_1` (normalized int16) | `ana` | `st.ana = float32(sp.Value) / 32768` (per-unit) – see OQ-O1 |
| `C_SE_NB_1` (scaled int16) | `ana` | `st.ana = float32(sp.Value)` |
| `C_SE_NC_1` (float32) | `ana` | `st.ana = sp.Value` |
| `C_BO_NA_1` | `bits` | already implemented (`st.bits = bc.Value`) |

Sketch (keep the existing `replyOrLog`/`confirm*` calls and the `[server] …` prints):

```go
case asdu.C_SC_NA_1, asdu.C_SC_TA_1:
    sc := pack.GetSingleCmd()
    st.mu.Lock(); st.spVal = sc.Value; st.mu.Unlock()
    fmt.Printf("[server] single command: IOA=%d value=%v COT=%v\n", sc.Ioa, sc.Value, pack.Identifier.Coa)
    return replyOrLog(conn, confirmSingleCmd(conn, pack.Type, st.ca, sc))

case asdu.C_SE_NC_1, asdu.C_SE_TC_1:
    sp := pack.GetSetpointFloatCmd()
    st.mu.Lock(); st.ana = sp.Value; st.mu.Unlock()
    fmt.Printf("[server] setpoint float command: IOA=%d value=%v COT=%v\n", sp.Ioa, sp.Value, pack.Identifier.Coa)
    return replyOrLog(conn, confirmSetpointFloat(conn, pack.Type, st.ca, sp))
```

`C_SE_TA_1`/`C_SE_TB_1`/`C_SE_TC_1`/`C_BO_TA_1` (IDs 58–64) are never reached:
`go-iecp5` drops those frames in `infoObjSize`/`fixInfoObjSize` before any handler runs
(documented Bug 3). Keep the code paths, but do not rely on them.

### O1a – Resolve the conflict with the 2 s spontaneous toggle

`spontaneousLoop` (`handler.go`) flips `spVal` every `-spontaneous` tick. A written
`C_SC_NA_1` value would therefore be overwritten within 2 s and the reported value would
no longer reflect the command.

**Decision: the toggle is disabled by default.** Add the flag

```go
toggle := flag.Bool("spontaneous-toggle", false, "flip the single point every tick (legacy demo behaviour)")
```

and change `spontaneousLoop` so that it only flips `spVal` when the flag is set; otherwise
it reports the current `st.spVal` unchanged on every `-spontaneous` tick. This makes
„send `C_SC` → read back“ reproducible, which is the whole point of the new 104-IN
semantics. The flag stays available as an opt-in for protocol demos; the repo README must
document the new default (the demo section currently promises an alternating value).

### O2 – Answer reads with the native object type (`handler.go`, `OnRead`)

Replace the unconditional `asdu.Single(...)` with a per-address dispatch:

| IOA | Object | Reply |
|---|---|---|
| 100 | `spVal` | `asdu.Single(conn, false, coa(asdu.Request), ca, SinglePointInfo{...})` → `M_SP_NA_1` |
| 200 | `dpVal` | `asdu.Double(...)` → `M_DP_NA_1` |
| 300 | normalized value | `asdu.MeasuredValueNormal(...)` → `M_ME_NA_1` |
| 301 | `ana` | `asdu.MeasuredValueFloat(...)` → `M_ME_NC_1` |
| 302 | `ana` + time | `sendMeasuredFloatTime(conn, asdu.Request, ca, ...)` (already exists in `confirm.go`) |
| 400 | `counter` | hand-built `M_IT_NA_1` (see below) |
| default | unknown | hand-built `C_RD_NA_1` with `COT = asdu.UnknownIOA` (47) |

Library constraint (verified in `asdu/mproc.go`): `Single`, `Double`,
`MeasuredValueNormal`, `MeasuredValueFloat` accept `COT = Request(5)`;
**`IntegratedTotals` does not** (only Spontaneous / 37–41). Therefore:

```go
func sendCounterRequest(conn asdu.Connect, cause asdu.Cause, ca asdu.CommonAddr, ioa asdu.InfoObjAddr, v asdu.BinaryCounterReading) error {
    u := newCommandASDU(conn, asdu.M_IT_NA_1, cause, ca)
    if err := u.AppendInfoObjAddr(ioa); err != nil { return err }
    u.AppendBinaryCounterReading(v)
    return conn.Send(u)
}
```

For „unknown IOA“ do **not** use `pack.SendReplyMirror(...)`: `GetReadCmd()` already
consumed the request buffer, so the mirror would be truncated (the repo README documents
this failure mode). Build it fresh:

```go
func confirmReadError(conn asdu.Connect, ca asdu.CommonAddr, cause asdu.Cause, ioa asdu.InfoObjAddr) error {
    u := newCommandASDU(conn, asdu.C_RD_NA_1, cause, ca)
    if err := u.AppendInfoObjAddr(ioa); err != nil { return err }
    return conn.Send(u)
}
```

### O3 – Per-request Common Address (**mandatory**)

The MSL5 plan stores the CA **per row** (`asduHigh`/`asduLow`). The outstation currently
answers every frame with the fixed `st.ca` from `-ca`. If MSL5 rows use several CAs, the
outstation must echo the CA of the request:

- keep the `*asdu.ASDU` parameter in `OnRead` (today `_ *asdu.ASDU`) and use
  `pack.CommonAddr` for the reply;
- in `OnASDU`, `OnInterrogation`, `OnCounterInterrogation` pass `pack.CommonAddr` to the
  `confirm*` builders instead of `st.ca`;
- `-ca` is used only for the periodic/spontaneous loops (they have no request). The MSL5
  rows that should receive these spontaneous frames must therefore use CA = `-ca`.

MSL5 no longer has a global/fallback CA: **every IN and OUT row carries its own CA** and the
master correlates answers by `(CA, IOA)`. An answer stamped with the wrong CA (`-ca`) is not
matched to its row, so O3 is **required** (MSL5 tests T5/T12 too). The library does not
filter by CA (it only rejects CA 0), so echoing `pack.CommonAddr` is all that is needed.
The process image stays a single one shared by all CAs (documented simplification).

### O4 – Logging for verification

Keep/extend the `[server] …` stdout lines; MSL5-side verification relies on them:

- `[server] read command: IOA=%d` (exists)
- `[server] single command: IOA=%d value=%v COT=%v` (exists)
- `[server] setpoint float command: IOA=%d value=%v COT=%v` (exists)
- add `[server] applied: spVal=%v dpVal=%v ana=%v bits=0x%08X` after each command so the
  process image is visible without `-verbose`.

---

## 4. Expected behaviour after the change

| MSL5 sends (IN row) | Outstation log | Answer MSL5 stores |
|---|---|---|
| `C_RD_NA_1` IOA 100 | `[server] read command: IOA=100` | `M_SP_NA_1` @100, COT 5, `value` bool |
| `C_RD_NA_1` IOA 301 | `[server] read command: IOA=301` | `M_ME_NC_1` @301, COT 5, `value` float |
| `C_RD_NA_1` IOA 400 | `[server] read command: IOA=400` | `M_IT_NA_1` @400, COT 5, `value` counter |
| `C_RD_NA_1` IOA 999 | `[server] read command: IOA=999` | `C_RD_NA_1` @999, COT 47 (unknown IOA) |
| `C_SC_NA_1` IOA 100 `true` | `[server] single command: IOA=100 value=true` | `C_SC_NA_1` @100, COT 7 (ActivationCon) |
| `C_DC_NA_1` IOA 200 `2` | `[server] double command: IOA=200 value=2` | `C_DC_NA_1` @200, COT 7 |
| `C_SE_NC_1` IOA 301 `42.5` | `[server] setpoint float command: IOA=301 value=42.5` | `C_SE_NC_1` @301, COT 7 |
| `C_BO_NA_1` IOA 400 `0x0000000F` | `[server] bitstring32 command: IOA=400 value=0x0000000F` | `C_BO_NA_1` @400, COT 7 |

Follow-up read after `C_SC`/`C_DC`/`C_SE` must return the **written** value (O1), and the
next GI (`C_IC_NA_1`) must report the same value (O1 + O1a).

---

## 5. Build & deployment steps

On the CM5 (or cross-build host with Go ≥ 1.27.1 as pinned in `go.mod`):

```bash
cd IEC104_Go
./build.sh                       # -> dist/iec104-server (linux/arm64) only
# optional smoke test on the host:
#   ./dist/iec104-server -host 127.0.0.1 -port 2404 -verbose
sudo ./deploy/install.sh         # user iec104, /opt/iec104, systemd unit, enable+start
```

Manual replace-only variant (when `/opt/iec104` already exists):

```bash
sudo systemctl stop iec104-server
sudo install -m 0755 -o iec104 -g iec104 dist/iec104-server /opt/iec104/iec104-server
sudo systemctl start iec104-server
sudo systemctl --no-pager --full status iec104-server
```

Notes:

- `install.sh` also disables and deletes the legacy master units
  (`iec104-client.service`, `iec104-client-demo.service`) and `/opt/iec104/iec104-client`;
  only one master may talk to the outstation at a time (the README states: only one
  master per outstation).
- `deploy/iec104-server.service` keeps `ExecStart=/opt/iec104/iec104-server -host 0.0.0.0
  -port 2404`: with the new default (`-spontaneous-toggle=false`, see O1a) the commanded
  values are no longer overwritten every 2 s. To restore the old alternating demo, start
  the unit/binary with `-spontaneous-toggle`.
- MSL5 reconnect: the outstation restart drops the TCP session; MSL5 reconnects
  automatically (`AutoConnect`, `Restart=always`).

---

## 6. Verification checklist

1. `journalctl -u iec104-server -f` in one terminal.
2. MSL5 → IEC 104 → Server: `state` must become `active` after the restart.
3. MSL5 → 104-IN: one row per command (CA high/low `0/1`, IOA 100/200/301/400), „Lesen“.
4. For each row, compare the outstation log line with the payload shown in
   „Antworten (Lesen)“ (table in §4).
5. Send `C_SC_NA_1` with value `true`, then `C_RD_NA_1` IOA 100 → payload must show
   `value: true`. Repeat with `false`.
6. Read an unused IOA (e.g. 999) → MSL5 must store a `C_RD_NA_1` payload with
   `cot.cause = 47`.
7. Regression: general interrogation from any standard master (the removed
   `iec104-client-demo` unit is no longer available; the live check in §11 used
   `wendy512/iec104`'s client) must still produce the five `M_*` frames and start
   the two reporting loops.

---

## 7. Risks & limitations

1. **Type IDs 58–64 stay unusable** (upstream decoder gap). Patching/vendoring
   `go-iecp5` would be the only fix; explicitly out of scope.
2. **Process image vs. spontaneous toggle**: resolved by O1a — the toggle is **off by
   default**, so commanded values persist. Only an explicitly started
   `-spontaneous-toggle` instance overwrites `spVal` every `-spontaneous` seconds.
3. **`C_SE_NA_1` normalization** is a decision, not a given (OQ-O1).
4. **Start/stop reporting loops**: `startReporting` is still bound to the first GI per
   connection; after an MSL5 reconnect the loops restart only if a GI is sent again.
5. **Multi-CA** requires O3 (mandatory now); otherwise answers are stamped with `-ca` and MSL5 cannot match them to the row.
6. Restarting the outstation kills the session; MSL5 shows `state = disconnected`
   briefly and reconnects.

---

## 8. Assumptions

- A1: MSL5 uses only the 1:1 commands listed in the MSL5 plan (no GI/counter
  interrogation from the UI), so `OnInterrogation`/`OnCounterInterrogation` remain
  demo-only entry points.
- A2: The IOA assignment stays 100/200/300/301/302/400; the MSL5 rows must use the same
  IOAs.
- A3: `-ca 1` is the default CA of the spontaneous/periodic reports; MSL5 has no CA setting any more, the row CA bytes must match it for those frames.
- A4: The outstation is built from `main` of `Mhd25OrCode/IEC104_Go` (no local forks).
- A5: `-spontaneous-toggle` defaults to `false`; the legacy alternating demo is opt-in.

---

## 9. Open questions (outstation)

**Resolved:** the former OQ-O2 (`-spontaneous-toggle`) is decided — **off by default**,
the flag remains as an opt-in (O1a).

- **OQ-O1**: Should `C_SE_NA_1` (normalized −1..1) be stored as per-unit
  (`/32768`), as raw int16, or rejected in favour of `C_SE_NC_1`? (Recommendation:
  store per-unit and document it.)
- **OQ-O3**: Should `OnResetProcess` (`C_RP_NA_1`) also reset the process image?

**Resolved:** OQ-O4 – O3 (echo the request CA) is required; MSL5 has no global CA.
- **OQ-O5**: Should the outstation answer `C_RD_NA_1` for IOA 302 with `M_ME_TF_1`
  (time-tagged) or with plain `M_ME_NC_1`? (Recommendation: `M_ME_TF_1`, consistent with
  `sendAllData`.)

---

## 10. Effort estimate

| Block | Effort |
|---|---|
| O1 + O1a (process image + toggle flag) | ~2 h |
| O2 (native read answers incl. counter/unknown-IOA builders) | ~3 h |
| O3 (per-request CA, mandatory) | ~1 h |
| O4 logging + README update | ~1 h |
| Build, deploy, verification on the device | ~1 h |
| Total | ~1 day |

---

## 11. Implementation status (2026-11-18)

All required changes **O1, O1a, O2, O3, O4** are implemented in
`iec104_server/`; the plan above is kept as the rationale.

| Plan item | Implementation |
| :-- | :-- |
| **O1** process image / no uncontrolled writes | `handler.go`: `periodicTick`/`spontaneousTick` only advance the image when `toggle` (`-spontaneous-toggle`) is set; commands are the only other writers |
| **O1a** `-spontaneous-toggle` flag | `main.go` (`flag.Bool`, default `false`), stored in `station.toggle` |
| **O2** native read answers | `handler.go OnRead`: IOA 100 → `M_SP_NA_1`, 200 → `M_DP_NA_1`, 300 → `M_ME_NA_1`, 301 → `M_ME_NC_1`, 302 → `M_ME_TF_1`, 400 → `M_IT_NA_1` (COT 5); anything else → `C_RD_NA_1` with COT 47 (`confirmReadError`) |
| **O3** echo the request CA (mandatory) | every handler uses `pack.CommonAddr`; `-ca` is only used by the unsolicited demo loops. GI/CI data and terminations included (`confirmInterrogation(..., ca, …)`, `sendAllData(conn, ca)`, `sendCounters(conn, ca)`) |
| **O4** logging | `logApplied()` prints the whole process image after each command; every handler logs type, IOA, COT and CA |
| helpers | `confirm.go`: `sendCounterRequest`, `confirmReadError`, `sendMeasuredFloatTime`, plus tagged support in `confirmSingleCmd`/`confirmDoubleCmd`/`confirmBitsString32Cmd`/`confirmSetpoint*` (the tagged types still cannot be decoded upstream, see §7.1) |
| tests | new `iec104_server/handler_test.go` (7 tests, see below) |

Decisions taken on the open questions:

* **OQ-O1 (`C_SE_NA_1`) — raw int16, not per-unit.** MSL5 sends engineering
  values for both int16 set-point forms; dividing by 32768 would make a
  `C_SE_NA_1` write read back 32768× smaller than what the operator entered.
  Documented in `README.md` ("Known limitations / deviations").
* **OQ-O5 (read of IOA 302) — `M_ME_TF_1`**, consistent with `sendAllData`.
* **OQ-O3 (`C_RP_NA_1`) — not implemented.** The command is acknowledged
  (`ActivationCon`) but does not reset the image; resetting would silently zero
  values the operator just wrote.
* Risk §7.4 (loops restart per connection only after a GI) is unchanged and
  deliberate: an MSL5 session that only writes and reads must not receive
  periodic/spontaneous traffic between a command and its read-back.

Verification (Windows, Go 1.27.4):

```text
go vet ./...     # clean
go test ./...    # ok  github.com/alfas/iec104-server
  TestCommandsApplyToProcessImage            (6 command types, type/COT 7/CA/IOA)
  TestReadAnswersWithNativeTypes             (6 native read answers)
  TestReadUnknownIOA                         (C_RD_NA_1, COT 47)
  TestReadBackAfterCommand                   (write → read returns the written value)
  TestInterrogationSequenceEchoesRequestCA   (7 → 20 → 10 on the request CA)
  TestCounterInterrogationEchoesRequestCA    (7 → 37 → 10 on the request CA)
  TestTicksRespectToggleFlag                 (no drift/flip without the flag)
```

A live session (real listener + `wendy512/iec104` master client, STARTDT
handshake) confirmed end to end: `C_SC_NA_1` → COT 7, `C_RD_NA_1` IOA 100 →
`M_SP_NA_1` COT 5 `false`, `C_SE_NC_1` 42.5 → COT 7, read IOA 301 →
`M_ME_NC_1` COT 5 `42.5`, read IOA 9999 → `C_RD_NA_1` COT 47.

Repository scope change in the same pass: the master application
(`iec104_app/`), its systemd demo unit, the NDJSON bridge documentation and the
`dist/iec104-client` artefact were removed — the repository now ships the
outstation only, and `build.sh` / `deploy/install.sh` install just
`iec104-server` (the installer also cleans up legacy client units and binary).
