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
server: `-period`/`-spontaneous` (reporting intervals), `-duration`.

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
| Periodic data | `M_ME_NC_1` | Periodic (1) | — |
| Spontaneous data | `M_SP_TB_1` | Spontaneous (3) | — |

Monitoring data sent in the interrogation response: `M_SP_NA_1`, `M_DP_NA_1`,
`M_ME_NA_1`, `M_ME_NC_1` (COT 20); counters `M_IT_NA_1` (COT 37).

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