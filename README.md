# IEC 60870-5-104 Go Test System

A minimal IEC 60870-5-104 (IEC 104) **master + outstation** test system written in Go,
built on [`wendy512/iec104`](https://github.com/wendy512/iec104) v1.0.4 and
[`wendy512/go-iecp5`](https://github.com/wendy512/go-iecp5) v1.2.5.

It demonstrates the core protocol fundamentals and implements the *correct*
confirmation sequences that the library's bundled example does **not**
(see `iec_60870_5_104_bug_report.md` for background).

## Layout

```
iec104_server/   outstation ("slave")
  main.go        flags, listener lifecycle
  handler.go     CommandHandler + process image + periodic/spontaneous reporting
  confirm.go     fresh-ASDU confirmation builders (the bug workaround)
iec104_client/   master ("client")
  main.go        connection + demo command sequence
  call.go        ASDUCall implementation (all replies & data)
```

## Build & run

```powershell
# build both - run each module's build from inside that module's folder
cd iec104_server; go build ./...      # writes .\iec104_server\iec104-server.exe
cd ..\iec104_client; go build ./...   # writes .\iec104_client\iec104-client.exe

# terminal 1 - outstation (runs until Ctrl+C, or pass -duration)
.\iec104_server\iec104-server.exe -host 0.0.0.0 -port 2404 -duration 60s

# terminal 2 - master (runs until Ctrl+C, or pass -duration)
.\iec104_client\iec104-client.exe -host 127.0.0.1 -port 2404 -duration 40s
```

The executable names come from the module paths (`github.com/alfas/iec104-server`
/ `-client`), not from the folder names, which is why `go build ./...` inside each
folder produces exactly the `iec104-server.exe` / `iec104-client.exe` used above.
Note that `cd iec104_server; go build ./...` followed by a plain `cd iec104_client`
fails - the second `cd` is relative to `iec104_server` - so use `cd ..\iec104_client`.

To capture a run to files, start the outstation with `Start-Process` redirection
instead of PowerShell's `2>` (in PowerShell 5.1 `2>` wraps the native stderr of the
client in a `NativeCommandError` record and writes UTF-16). This is exactly how the
2026-09-25 run below was captured:

```powershell
$srv = Start-Process .\iec104_server\iec104-server.exe -ArgumentList '-host','0.0.0.0','-port','2404','-duration','60s' `
       -RedirectStandardOutput logs\srv.out -RedirectStandardError logs\srv.err -PassThru -NoNewWindow
Start-Sleep -Seconds 2
cmd /c ".\iec104_client\iec104-client.exe -host 127.0.0.1 -port 2404 -duration 40s 1> logs\client.out 2> logs\client.err"
$srv.WaitForExit()
```

Flags: `-host`, `-port`, `-ca` (common address, default 1),
server: `-period`/`-spontaneous` (reporting intervals), `-duration`,
both: `-verbose` (enable the go-iecp5 frame/decode log, off by default).

## Protocol exchanges exercised

| Direction | ASDU | Cause out | Cause back |
| :-- | :-- | :-- | :-- |
| General interrogation | `C_IC_NA_1` | Activation (6) | ActivationCon (7) → data (20) → ActivationTerm (10) |
| Counter interrogation | `C_CI_NA_1` | Activation (6) | ActivationCon (7) → totals (37) → ActivationTerm (10) |
| Read | `C_RD_NA_1` | Request (5) | data with COT=Request (5) |
| Clock sync | `C_CS_NA_1` | Activation (6) | ActivationCon (7), time echoed |
| Test | `C_TS_TA_1` | Activation (6) | ActivationCon (7), test word |
| Reset process | `C_RP_NA_1` | Activation (6) | ActivationCon (7) |
| Delay acquisition | `C_CD_NA_1` | Activation (6) | ActivationCon (7) |
| Single / Double / Set-point | `C_SC_NA_1` / `C_DC_NA_1` / `C_SE_*_1` | Activation (6) | ActivationCon (7) |
| Bitstring-32 (untagged) | `C_BO_NA_1` | Activation (6) | ActivationCon (7) |
| Bitstring-32 + CP56Time2a | `C_BO_TA_1` | Activation (6) | ActivationCon (7) — frame dropped by the unpatched library, see §14 of the bug report |
| Periodic data | `M_ME_NC_1` | Periodic (1) | — |
| Spontaneous data | `M_SP_TB_1` + `M_ME_TF_1` (CP56-tagged) | Spontaneous (3) | — |

Monitoring data sent in the interrogation response: `M_SP_NA_1`, `M_DP_NA_1`,
`M_ME_NA_1`, `M_ME_NC_1`, `M_ME_TF_1` (COT 20); counters `M_IT_NA_1` (COT 37).
Background reporting: `M_ME_NC_1` IOA 301 (COT 1) every `-period`, plus
`M_SP_TB_1` IOA 100 and `M_ME_TF_1` IOA 302 (COT 3) every `-spontaneous`.
IOA 302 carries the *same* analog value as IOA 301 (the spontaneous loop reads the
process image that the periodic loop advances), and that analog drifts by +12.5 per
period and wraps to -30 once it would exceed 220 — so a long run eventually reports
negative values on both IOAs.

## Verified run (2026-09-25)

The two commands above were re-run unchanged (outstation `-duration 60s`, master
`-duration 40s`, logs kept in `logs/`), and every claim in this README and in
`iec_60870_5_104_bug_report.md` was re-checked against the result.

| Observation | Result |
| :-- | :-- |
| Demo command sequence | 11/11 sent after STARTDT; every expected confirmation arrived with `COT=ActivationCon(7)` |
| General interrogation | `ActivationCon(7)` → `M_SP_NA_1`/`M_DP_NA_1`/`M_ME_NA_1`/`M_ME_NC_1`/`M_ME_TF_1` (COT 20) → `ActivationTerm(10)` |
| Counter interrogation | `ActivationCon(7)` → `M_IT_NA_1` IOA 400 (COT 37) → `ActivationTerm(10)` |
| Read | `M_SP_NA_1` IOA 100 with `COT=Request(5)` |
| `C_BO_NA_1` (51) | decoded by the peer, `ActivationCon` received, IOA 4001 = `0x0F0F0F0F` |
| `C_BO_TA_1` (64) | dropped by the peer's decoder — no handler call, no confirmation (§14) |
| Background reporting over 40 s | 13 × `M_ME_NC_1` IOA 301 (COT 1) + 19 × `M_SP_TB_1` IOA 100 and 19 × `M_ME_TF_1` IOA 302 (COT 3) |
| Analog drift / wrap | IOA 301 and 302 walked 100.000 → 212.500 → **-30.000** → -17.5 → -5.0 → 7.5 |
| Shutdown | client `StopDt` at 40 s; outstation exits at 60 s (`server stopped`) |
| Client TCP connections per session | 2 (the first is closed immediately by the library's auto-connect) — normal, also visible in earlier runs |

Master side of that run:

```text
[client] general interrogation sent
[client] interrogation reply: IOA=0 QOI=20 COT=COT<ActivationCon>
[client] single point:    IOA=100 value=true     QDS=0 COT=COT<InterrogatedByStation>
[client] double point:    IOA=200 value=2        QDS=0 COT=COT<InterrogatedByStation>
[client] measured normal: IOA=300 value=0.305176 QDS=0 COT=COT<InterrogatedByStation>
[client] measured float:  IOA=301 value=100.000  QDS=0 COT=COT<InterrogatedByStation> type=TID<M_ME_NC_1>
[client] measured float:  IOA=302 value=100.000  QDS=0 COT=COT<InterrogatedByStation> type=TID<M_ME_TF_1>
[client] interrogation reply: IOA=0 QOI=20 COT=COT<ActivationTerm>
[client] counter interrogation reply: IOA=0 QCC=0x05 COT=COT<ActivationCon>
[client] integrated totals: IOA=400 count=0 SQ=1 CY=false CA=false IV=false COT=COT<RequestByGeneralCounter>
[client] counter interrogation reply: IOA=0 QCC=0x05 COT=COT<ActivationTerm>
[client] single point: IOA=100 value=true QDS=0 COT=COT<Request>
[client] clock sync reply: IOA=0 time=2026-09-25T13:38:58Z COT=COT<ActivationCon>
[client] test command reply (CP56): IOA=0 valid-test-word=true COT=COT<ActivationCon>
[client] reset process reply: IOA=0 QRP=0x01 COT=COT<ActivationCon>
[client] single command confirmation: IOA=1000 value=true QOC=0 COT=COT<ActivationCon>
[client] double command confirmation: IOA=2000 value=2 QOC=0 COT=COT<ActivationCon>
[client] setpoint float confirmation: IOA=3000 value=125.500 COT=COT<ActivationCon>
[client] bitstring32 command confirmation: IOA=4001 value=0x0F0F0F0F COT=COT<ActivationCon>
[client] measured float: IOA=301 value=212.500 QDS=0 COT=COT<Periodic>    type=TID<M_ME_NC_1>
[client] measured float: IOA=301 value=-30.000 QDS=0 COT=COT<Periodic>    type=TID<M_ME_NC_1>
[client] measured float: IOA=302 value=-30.000 QDS=0 COT=COT<Spontaneous> type=TID<M_ME_TF_1>
```

With `-verbose` the same session shows the silent type-64 drop next to the working
type-51 twin:

```text
cs104 server => [D]: RX Raw[68 18 12 00 00 00 40 01 06 00 01 00 a0 0f 00 ff 00 ff 00 ...]  <- C_BO_TA_1 (64)
cs104 server => [E]: asdu UnmarshalBinary failed,asdu: type identification unknown
cs104 server => [D]: RX Raw[68 11 14 00 00 00 33 01 06 00 01 00 a1 0f 00 0f 0f 0f 0f]     <- C_BO_NA_1 (51)
cs104 server => [D]: ASDU TID<C_BO_NA_1> COT<Activation> @1
[server] bitstring32 command: IOA=4001 value=0x0F0F0F0F COT=COT<Activation>
```

Without `-verbose` the outstation log holds only the `[server]` application lines
(716 bytes for the whole 60 s run) — the dropped frame leaves no trace at all,
which is why the flag exists.

## Key implementation point (the workaround)

`go-iecp5`'s `ASDU.Get*Cmd()` decoders *consume* the internal info-object
buffer, and the cs104 server dispatcher calls them **before** the application
handler runs. Therefore a handler must **never** respond with
`pack.SendReplyMirror(...)` after parsing — the mirrored ASDU would be empty /
truncated. Instead:

1. parse the request with `pack.Get*Cmd()` to extract the parameters, then
2. build a brand-new ASDU (`asdu.NewASDU` + `AppendInfoObjAddr` +
   `AppendBytes/AppendFloat32/...`) with `Cause = ActivationCon`, and send it.

This is implemented in `iec104_server/confirm.go`. It also sidesteps the fact
that the library's command constructors (`InterrogationCmd`,
`ClockSynchronizationCmd`, ...) *force* `Activation` and therefore cannot
produce `ActivationCon` confirmations.

The same rule drives `sendMeasuredFloatTime` (hand-built `M_ME_TF_1`):
`asdu.MeasuredValueFloatCP56Time2a` accepts only COT Spontaneous/Request, and
`asdu.BitsString32Cmd` only `Activation`/`Deactivation`, so both confirmations
and the GI-time-tagged measurement are assembled by hand.

## Known limitation — `C_BO_TA_1` (bug report §14)

`go-iecp5`'s `asdu.infoObjSize` table has **no entry** for `C_BO_TA_1` (nor for
`C_SC_TA_1`, `C_DC_TA_1`, `C_RC_TA_1`, `C_SE_TA_1/TB_1/TC_1`), so a receiver drops
such frames inside `UnmarshalBinary` (`asdu: type identification unknown`) before
any handler runs, and the sender gets no error. The failure is silent unless
`-verbose` is used. Re-verified on 2026-09-25 (see the run section above): the
type-64 frame is still dropped while the type-51 twin completes its round-trip.
The complete `C_BO_TA_1` code path is implemented and ready;
because the table is unexported and there is no registration API, only a patched
library makes the round-trip work — until then the demo also sends the
registered, working `C_BO_NA_1`.