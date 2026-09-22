# IEC 60870-5-104 Go Test System — Architecture & Workflow

Documentation for the IEC 104 master + outstation test system in this repository
(see also the root [`README.md`](../README.md)).

- **Library stack:** [`wendy512/iec104`](https://github.com/wendy512/iec104) v1.0.4
  (IEC 104 transport layer) and [`wendy512/go-iecp5`](https://github.com/wendy512/go-iecp5)
  v1.2.5 (ASDU codec), Go 1.27, two independent Go modules.
- **Transport:** one TCP connection per session, default port **2404**,
  default common address (CA) **1**.

---

## 1. Architecture

### 1.1 High level

Two cooperating programs exchange IEC 104 frames over one TCP connection:

```text
┌──────────────────────────┐      TCP 2404 (IEC 104)       ┌──────────────────────────┐
│  iec104_client  (master) │ ◄───────────────────────────► │ iec104_server (outstation)│
│                          │  commands (C_*)               │                          │
│  main.go                 │  ───────────────────────────► │ main.go                  │
│   flags, connect, demo   │                               │  flags, listener,        │
│   command sequence       │  confirmations & data         │  lifecycle               │
│                          │  (M_*, ActivationCon/Term)    │                          │
│  call.go                 │  ◄─────────────────────────── │ handler.go               │
│   client.ASDUCall impl:  │                               │  server.CommandHandler:  │
│   all inbound callbacks  │                               │  process image, GI / CI, │
│                          │                               │  periodic + spontaneous  │
│                          │                               │ confirm.go               │
│                          │                               │  fresh-ASDU confirmation │
│                          │                               │  builders (bug workaround)│
└──────────────────────────┘                               └──────────────────────────┘
```

### 1.2 Repository layout

| File | Role |
| :--- | :--- |
| `iec104_server/main.go` | Flags (`-host`, `-port`, `-ca`, `-period`, `-spontaneous`, `-duration`), listener lifecycle, graceful shutdown on `-duration` or Ctrl+C/SIGTERM |
| `iec104_server/handler.go` | `station` — implements `server.CommandHandler`: process image, general/counter interrogation, read/clock-sync/reset/delay/test handlers, control-command dispatch, background reporting goroutines |
| `iec104_server/confirm.go` | Confirmation builders that construct **fresh ASDUs** — the `SendReplyMirror()` bug workaround (see §4) |
| `iec104_client/main.go` | Flags (`-host`, `-port`, `-ca`, `-duration`), connection settings (`AutoConnect`), demo command sequence fired after STARTDT |
| `iec104_client/call.go` | `masterCall` — implements `client.ASDUCall`: logs every reply, confirmation and monitoring value |

### 1.3 Layering

- **`wendy512/iec104` (transport):** TCP server/client, APDU framing, I-/S-/U-format
  frames, sequence numbers, STARTDT/STOPDT/TESTFR handshakes, t0–t3 timeouts,
  automatic re-connect (`AutoConnect = true` on the client).
- **`wendy512/go-iecp5/asdu` (application layer):** ASDU identifier
  (type, variable structure, cause of transmission, common address) plus typed
  encode/decode helpers (`Get*Cmd()`, `Append*`, and `M_*`/`C_*` constructors).
- **This repository (application code):** `station` (outstation behavior) and
  `masterCall` (master behavior) work purely on ASDUs — no frame-level code.

### 1.4 Server: process image & handlers

`station` (in `handler.go`) simulates an outstation with a small process image
guarded by `sync.Mutex`:

| IOA | Signal | Updated by |
| :-- | :-- | :-- |
| 100 | single point (`bool`) | `spontaneousLoop` (toggle) and read replies |
| 200 | double point (`DPIDeterminedOn`) | fixed at startup |
| 300 | normalized measurement (`Normalize(10000)`) | fixed value in the GI response |
| 301 | short-float measurement | `periodicLoop` (drifts +12.5, wraps 220 → −30) |
| 400 | integrated totals counter | static in this demo |

Implemented handler methods: `OnInterrogation`, `OnCounterInterrogation`,
`OnRead`, `OnClockSync`, `OnResetProcess`, `OnDelayAcquisition`,
`OnTestCommand`, and `OnASDU` (control commands: single / double / set-points /
`C_TS_TA_1`).

`startReporting` is idempotent per remote address (`sync.Map`) and is invoked
from `OnInterrogation` (i.e. only after STARTDT is active). It launches, per
session:

- `periodicLoop` — `M_ME_NC_1` IOA 301, COT = Periodic(1), every `-period`
  (default 3 s).
- `spontaneousLoop` — `M_SP_TB_1` IOA 100, COT = Spontaneous(3), every
  `-spontaneous` (default 2 s), toggling the single point.

Both loops tolerate up to 30 consecutive send failures before stopping, so a
dead socket cannot spin forever.

### 1.5 Server: the fresh-ASDU confirmation layer

`confirm.go` implements the workaround mandated by the library bugs (§4):

- `newCommandASDU` — builds `asdu.NewASDU` with `SQ=0, Number=1` and the
  requested `Cause` (`ActivationCon`, `ActivationTerm`, `DeactivationCon`, …).
- `confirm*` builders for `C_IC_NA_1`, `C_CI_NA_1`, `C_CS_NA_1`, `C_RP_NA_1`,
  `C_CD_NA_1`, `C_TS_TA_1`, `C_SC_NA_1/TA_1`, `C_DC_NA_1/TA_1`,
  `C_SE_NA_1/TA_1`, `C_SE_NB_1/TB_1`, `C_SE_NC_1/TC_1`.

Every confirmation is a **brand-new ASDU** assembled with
`AppendInfoObjAddr` + `AppendBytes / AppendNormalize / AppendScaled /
AppendFloat32 / AppendCP56Time2a / AppendCP16Time2a` — never
`SendReplyMirror()`. This also sidesteps the library's command constructors
(`InterrogationCmd`, `ClockSynchronizationCmd`, `SingleCmd`, …), which force
`Cause = Activation` and therefore cannot emit `ActivationCon` confirmations.

### 1.6 Client: callbacks

`masterCall` (in `call.go`) implements `client.ASDUCall`:

| Callback | Handles |
| :--- | :--- |
| `OnInterrogation` | `C_IC_NA_1` replies (ActivationCon / ActivationTerm) |
| `OnCounterInterrogation` | `C_CI_NA_1` replies |
| `OnRead` | echoed `C_RD_NA_1` (normally unused — the server answers with data) |
| `OnTestCommand` | untagged `C_TS_NA_1` replies |
| `OnClockSync`, `OnResetProcess`, `OnDelayAcquisition` | respective confirmations |
| `OnASDU` | everything else: monitoring data (single / double / measured-normal / measured-float / integrated totals) and command confirmations via `onCommandReply` (`C_TS_TA_1`, `C_SC_*`, `C_DC_*`, `C_SE_*`) |

---

## 2. Workflow

### 2.1 Session lifecycle

```text
server: Start() → listens on host:port
client: Connect()  (AutoConnect = true)
   │
   ├─ TCP connect                       [server] client connected
   ├─ U-format STARTDT / STARTDT_con    (handled inside wendy512/iec104)
   ▼
client SetServerActiveHandler fires: "outstation active"
   └─ sendDemoCommands()  — see §2.2
   │
   ├─ server background reporting running (see §2.3)
   ▼
run until -duration elapses or Ctrl+C / SIGTERM
   ▼
client Close()  /  server Stop()
```

### 2.2 Demo command sequence (client → server)

Fired once STARTDT is confirmed, in this order:

| # | Command | Client call | Target |
| :-- | :-- | :-- | :-- |
| 1 | General interrogation | `SendInterrogationCmd(ca)` | QOI = 20 |
| 2 | Counter interrogation | `SendCounterInterrogationCmd(ca)` | — |
| 3 | Read | `SendReadCmd(ca, 100)` | IOA 100 |
| 4 | Clock synchronization | `SendClockSynchronizationCmd(ca, time.Now())` | — |
| 5 | Test command | `SendTestCmd(ca)` | — |
| 6 | Reset process | `SendResetProcessCmd(ca)` | — |
| 7 | Single command | `SendCmd(ca, C_SC_NA_1, 1000, true)` | IOA 1000 |
| 8 | Double command | `SendCmd(ca, C_DC_NA_1, 2000, DPIDeterminedOn)` | IOA 2000 |
| 9 | Set-point float | `SendCmd(ca, C_SE_NC_1, 3000, 125.5)` | IOA 3000 |

### 2.3 Typical exchanges

**General interrogation** — the full standard three-step sequence:

```text
client                                              server (station.OnInterrogation)
  │ C_IC_NA_1  COT=Activation(6)  QOI=20
  │ ───────────────────────────────────────────────►  startReporting() ← first GI only
  │ C_IC_NA_1  COT=ActivationCon(7)  QOI=20
  │ ◄───────────────────────────────────────────────
  │ M_SP_NA_1 / M_DP_NA_1 / M_ME_NA_1 / M_ME_NC_1
  │   COT=InterrogatedByStation(20)                   sendAllData()
  │ ◄───────────────────────────────────────────────
  │ C_IC_NA_1  COT=ActivationTerm(10)  QOI=20
  │ ◄───────────────────────────────────────────────
```

A `Deactivation(8)` GI is answered with `DeactivationCon(9)` instead.

**Counter interrogation** — same three-step pattern:
`C_CI_NA_1 Activation` → `ActivationCon` → `M_IT_NA_1` (IOA 400, COT 37) →
`ActivationTerm`.

**All other commands** — request `COT=Activation(6)`, then one fresh
`ActivationCon(7)` confirmation echoing the request parameters
(`C_CS_NA_1` echoes the CP56Time2a; `C_TS_TA_1` echoes the FBP test word +
time; `C_CD_NA_1` echoes the CP16Time2a). A **read** command is answered with
the current value as `M_SP_NA_1 COT=Request(5)`.

**Background reporting** (per session, after the first GI):

```text
periodicLoop    : every -period      → M_ME_NC_1  IOA 301  COT=Periodic(1)     value += 12.5
spontaneousLoop : every -spontaneous → M_SP_TB_1  IOA 100  COT=Spontaneous(3)  toggle bool
```

### 2.4 Build & run

```powershell
# build both
cd iec104_server; go build ./...
cd iec104_client; go build ./...

# terminal 1 — outstation (runs until Ctrl+C, or pass -duration)
.\iec104_server\iec104-server.exe -host 0.0.0.0 -port 2404 -duration 60s

# terminal 2 — master
.\iec104_client\iec104-client.exe -host 127.0.0.1 -port 2404 -duration 40s
```

## 3. ASDUs used

### 3.1 Control direction (master → outstation)

| Type ID | Name | COT(s) received | Handled by |
| :-- | :-- | :-- | :-- |
| `C_IC_NA_1` | General interrogation | Activation(6), Deactivation(8) | `station.OnInterrogation` |
| `C_CI_NA_1` | Counter interrogation | Activation(6) | `station.OnCounterInterrogation` |
| `C_RD_NA_1` | Read | Request(5) | `station.OnRead` |
| `C_CS_NA_1` | Clock synchronization | Activation(6) | `station.OnClockSync` |
| `C_TS_NA_1` / `C_TS_TA_1` | Test command (untagged / with CP56Time2a) | Activation(6) | `station.OnTestCommand` / `OnASDU` |
| `C_RP_NA_1` | Reset process | Activation(6) | `station.OnResetProcess` |
| `C_CD_NA_1` | Delay acquisition | Activation(6) | `station.OnDelayAcquisition` |
| `C_SC_NA_1` / `C_SC_TA_1` | Single command | Activation(6) | `station.OnASDU` |
| `C_DC_NA_1` / `C_DC_TA_1` | Double command | Activation(6) | `station.OnASDU` |
| `C_SE_NA_1` / `C_SE_TA_1` | Set-point, normalized | Activation(6) | `station.OnASDU` |
| `C_SE_NB_1` / `C_SE_TB_1` | Set-point, scaled | Activation(6) | `station.OnASDU` (demo client sends only `C_SE_NC_1`) |
| `C_SE_NC_1` / `C_SE_TC_1` | Set-point, short float | Activation(6) | `station.OnASDU` |

### 3.2 Monitoring direction (outstation → master)

| Type ID | Name | COT(s) | Produced by |
| :-- | :-- | :-- | :-- |
| `M_SP_NA_1` | Single point | InterrogatedByStation(20), Request(5) | `sendAllData`, `OnRead` |
| `M_SP_TB_1` | Single point + CP56Time2a | Spontaneous(3) | `spontaneousLoop` |
| `M_DP_NA_1` | Double point | InterrogatedByStation(20) | `sendAllData` |
| `M_ME_NA_1` | Measured value, normalized | InterrogatedByStation(20) | `sendAllData` |
| `M_ME_NC_1` | Measured value, short float | InterrogatedByStation(20), Periodic(1) | `sendAllData`, `periodicLoop` |
| `M_IT_NA_1` | Integrated totals | RequestByGeneralCounter(37) | `sendCounters` |

### 3.3 Confirmations sent by the server

All command confirmations are **fresh ASDUs** with `COT = ActivationCon(7)`:
`C_IC_NA_1`, `C_CI_NA_1` additionally send `ActivationTerm(10)` after the data,
and `C_IC_NA_1` also supports `DeactivationCon(9)`. The confirmations echo the
request parameters (IOA, value, QOS/QOC, time tags) — see `confirm.go` and §4.

### 3.4 Causes of transmission in use

| COT | Name | Where |
| :-- | :-- | :-- |
| 1 | Periodic | periodic analog reports |
| 3 | Spontaneous | spontaneous single-point reports |
| 5 | Request | read replies |
| 6 | Activation | client command requests |
| 7 | ActivationCon | server command confirmations |
| 8 / 9 | Deactivation / DeactivationCon | GI deactivation path |
| 10 | ActivationTerm | GI / CI termination |
| 20 | InterrogatedByStation | GI data |
| 37 | RequestByGeneralCounter | counter data |

U-format system frames (`STARTDT`, `STOPDT`, `TESTFR`) are handled entirely
inside `wendy512/iec104`; application code never touches them.

## 4. Notice about `iec_60870_5_104_bug_report.md`

The repository root contains **`iec_60870_5_104_bug_report.md`** — a verified
bug report for `wendy512/go-iecp5` / `wendy512/iec104` that **directly
constrains how this codebase is written**. Read it before modifying the
server's response logic.

### Bug 1 — `SendReplyMirror()` after `Get*Cmd()` produces corrupted ASDUs

The `Get*Cmd()` decoders (e.g. `GetSingleCmd`, `GetInterrogationCmd`) consume /
advance the internal info-object buffer, and the library's cs104 dispatcher
calls them **before** the application handler runs. A mirrored ASDU
(`pack.SendReplyMirror(...)`) is therefore built from already-consumed data and
arrives empty / truncated; the peer fails with
`asdu UnmarshalBinary failed, EOF`.

**Workaround used here:** never mirror. Parse the request only to *read* the
parameters, then build a **brand-new ASDU** with `Cause = ActivationCon` and
send it. This is implemented for every command in
`iec104_server/confirm.go` (`newCommandASDU` + the `confirm*` builders).

### Bug 2 — General interrogation sequence (resolved in this repository)

The library's bundled example could not produce the standard GI sequence
(`ActivationCon` → data → `ActivationTerm`). This repository resolves it with
the explicit three-step sequence in `station.OnInterrogation`
(`handler.go`), verified end-to-end in the live logs:

```text
[client] interrogation reply: IOA=0 QOI=20 COT=COT<ActivationCon>
[client] single point:    IOA=100 value=true  ... COT=COT<InterrogatedByStation>
[client] double point:    IOA=200 value=2     ... COT=COT<InterrogatedByStation>
[client] measured normal: IOA=300 value=0.305 ... COT=COT<InterrogatedByStation>
[client] measured float:  IOA=301 value=100.0 ... COT=COT<InterrogatedByStation>
[client] interrogation reply: IOA=0 QOI=20 COT=COT<ActivationTerm>
```

### Additional library constraint

The library's command constructors (`InterrogationCmd`,
`ClockSynchronizationCmd`, `CounterInterrogationCmd`, `ResetProcessCmd`,
`DelayAcquireCommand`, `SingleCmd`, …) **force** `Cause = Activation` or reject
`ActivationCon`, so they cannot emit proper confirmations — another reason all
confirmations are hand-built in `confirm.go`.

### Rule of thumb

> Treat an ASDU as consumed after calling `Get*Cmd()`. Do not mirror it
> afterward — use the received ASDU to extract information, then construct a
> **new** ASDU for the response.

See `iec_60870_5_104_bug_report.md` for the full analysis, the list of
verified-working functions, and the updated status table.




