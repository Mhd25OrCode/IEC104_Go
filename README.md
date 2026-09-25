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
# build both
cd iec104_server; go build ./...
cd iec104_client; go build ./...

# terminal 1 - outstation (runs until Ctrl+C, or pass -duration)
.\iec104_server\iec104-server.exe -host 0.0.0.0 -port 2404 -duration 60s

# terminal 2 - master (runs until Ctrl+C, or pass -duration)
.\iec104_client\iec104-client.exe -host 127.0.0.1 -port 2404 -duration 40s
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
`-verbose` is used. The complete `C_BO_TA_1` code path is implemented and ready;
because the table is unexported and there is no registration API, only a patched
library makes the round-trip work — until then the demo also sends the
registered, working `C_BO_NA_1`.