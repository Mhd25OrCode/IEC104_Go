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
                       stdin/stdout (NDJSON)          ┌──────────────────────────┐      TCP 2404 (IEC 104)       ┌──────────────────────────┐
                       child process                   │  iec104_client  (master) │ ◄───────────────────────────► │ iec104_server (outstation)│
   NestJS backend  ◄──────────────────────────────────►│                          │  commands (C_*)               │                          │
   (external, not   dispatch(points) → ack                │  main.go                 │  ───────────────────────────► │ main.go                  │
   in this repo)    ← event frames                      │   flags, connect,        │                               │  flags, listener,        │
                                                        │   -demo command seq.     │  confirmations & data         │  lifecycle               │
                                                        │                          │  (M_*, ActivationCon/Term)    │                          │
                                                        │  call.go                 │  ◄─────────────────────────── │ handler.go               │
                                                        │   client.ASDUCall impl:  │                               │  server.CommandHandler:  │
                                                        │   all inbound callbacks  │                               │  process image, GI / CI, │
                                                        │   → bridge.publishEvent  │                               │  periodic + spontaneous  │
                                                        │                          │                               │ confirm.go               │
                                                        │  bridge.go               │                               │  fresh-ASDU confirmation │
                                                        │   NDJSON reader/writer,  │                               │  builders (bug workaround)│
                                                        │   registry, event queue  │                               │                          │
                                                        │  typemap.go              │                               │                          │
                                                        │   asduTyp <-> TypeID     │                               │                          │
                                                        └──────────────────────────┘                               └──────────────────────────┘
```

The stdio bridge is an optional adapter bolted onto the existing master: the
MSL5 backend spawns the client binary as a **child process** (both run on the
same Raspberry Pi) and talks to it over **stdin/stdout using newline-delimited
JSON (NDJSON)**. That lets the backend drive the same `*client.Client` that the
built-in `-demo` command sequence uses, and observe every inbound ASDU as a
stream of `event` frames. Full detail (wire protocol, `typemap.go`, `bridge.go`
internals) lives in [`IEC104_GO_BRIDGE.md`](../IEC104_GO_BRIDGE.md) and
[`iec104_client_architecture.md`](iec104_client_architecture.md) §7; this
document only covers how it fits into the whole system.

### 1.2 Repository layout

| File | Role |
| :--- | :--- |
| `iec104_server/main.go` | Flags (`-host`, `-port`, `-ca`, `-period`, `-spontaneous`, `-duration`), listener lifecycle, graceful shutdown on `-duration` or Ctrl+C/SIGTERM |
| `iec104_server/handler.go` | `station` — implements `server.CommandHandler`: process image, general/counter interrogation, read/clock-sync/reset/delay/test handlers, control-command dispatch, background reporting goroutines |
| `iec104_server/confirm.go` | Confirmation builders that construct **fresh ASDUs** — the `SendReplyMirror()` bug workaround (see §4) |
| `iec104_client/main.go` | Flags (`-host`, `-port`, `-ca`, `-duration`, `-verbose`, `-demo`, `-version`), connection settings (`AutoConnect`), keeps stdout for protocol frames only (everything else goes to stderr), starts the stdio bridge, shuts down on stdin EOF / SIGTERM, optional demo command sequence fired after STARTDT (only with `-demo`) |
| `iec104_client/call.go` | `masterCall` — implements `client.ASDUCall`: logs every reply, confirmation and monitoring value, and forwards each one to `bridge.publishEvent` |
| `iec104_client/bridge.go` | `bridge` — the NDJSON stdio protocol (`ready`, `dispatch` → `ack`, `event`); `point` is the JSON form of the backend's `Iec104Dto`; `registry` correlates `(CommonAddr, InfoObjAddr)` to the last dispatched point; one writer goroutine owns stdout and drains a single non-blocking event channel |
| `iec104_client/bridge_test.go` | Pipe-based round-trip tests (`ready`, `dispatch` → `ack`, events, EOF shutdown, stdout contains only JSON lines) |
| `iec104_client/typemap.go` | `asduTyp` string ↔ `asdu.TypeID` conversion tables, plus `commandValueFromPayload` (decodes the `payload` object into the Go type `SendCmd` expects) |
| `build.sh` | Cross-builds the static `linux/arm64` binaries (`CGO_ENABLED=0`, `-trimpath -s -w`) into `dist/iec104-{server,client}` for the Raspberry Pi CM4/CM5 |
| `pull.sh` | Token-authenticated `git fetch` + `git merge --ff-only` update from GitHub (PAT read from `$HOME/.iec104-git-token`); the token is never written to `.git/config` |
| `deploy/install.sh`, `deploy/uninstall.sh` | Install / remove `/opt/iec104/` plus the systemd units (must run as root) |
| `deploy/iec104-*.service` | systemd units: `iec104-server` (`enable --now`) and `iec104-client-demo` (manual-only `-demo` master). The bridge client has no unit: the backend spawns `/opt/iec104/iec104-client` (`IEC104_BRIDGE_BIN`) |
| `docs/architecture.md`, `docs/iec104_client_architecture.md` | This document and the client-side deep dive |

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
| 301 | short-float measurement (`M_ME_NC_1`) | `periodicLoop` (drifts +12.5, wraps 220 → −30) |
| 302 | time-tagged short-float measurement (`M_ME_TF_1`) | `spontaneousLoop` and the GI response (mirrors IOA 301) |
| 400 | integrated totals counter | static in this demo |
| — | bitstring-32 command output (`uint32`) | `OnASDU` (`C_BO_NA_1` / `C_BO_TA_1`), stored only |

Implemented handler methods: `OnInterrogation`, `OnCounterInterrogation`,
`OnRead`, `OnClockSync`, `OnResetProcess`, `OnDelayAcquisition`,
`OnTestCommand`, and `OnASDU` (control commands: single / double / set-points /
bitstring-32 (`C_BO_NA_1` / `C_BO_TA_1`) / `C_TS_TA_1`).

`OnASDU` is the only handler that both consumes the received ASDU
(`pack.Get*Cmd()`) **and** returns an error to cs104's dispatcher, which answers
a non-nil error with `SendReplyMirror(UnknownTypeID)` — a mirror of an
already-consumed ASDU. Its responses therefore go through `replyOrLog()`, which
logs a failed send instead of propagating it.

`startReporting` is idempotent per remote address (`sync.Map`) and is invoked
from `OnInterrogation` (i.e. only after STARTDT is active). It launches, per
session:

- `periodicLoop` — `M_ME_NC_1` IOA 301, COT = Periodic(1), every `-period`
  (default 3 s).
- `spontaneousLoop` — `M_SP_TB_1` IOA 100, COT = Spontaneous(3), every
  `-spontaneous` (default 2 s), toggling the single point, followed by the
  CP56-tagged analog `M_ME_TF_1` IOA 302.

Both loops tolerate up to 30 consecutive send failures before stopping, so a
dead socket cannot spin forever.

### 1.5 Server: the fresh-ASDU confirmation layer

`confirm.go` implements the workaround mandated by the library bugs (§4):

- `newCommandASDU` — builds `asdu.NewASDU` with `SQ=0, Number=1` and the
  requested `Cause` (`ActivationCon`, `ActivationTerm`, `DeactivationCon`, …).
- `confirm*` builders for `C_IC_NA_1`, `C_CI_NA_1`, `C_CS_NA_1`, `C_RP_NA_1`,
  `C_CD_NA_1`, `C_TS_TA_1`, `C_SC_NA_1/TA_1`, `C_DC_NA_1/TA_1`,
  `C_SE_NA_1/TA_1`, `C_SE_NB_1/TB_1`, `C_SE_NC_1/TC_1`, `C_BO_NA_1/TA_1`.
- `sendMeasuredFloatTime` — reports `M_ME_TF_1` (CP56-tagged short float) as a
  fresh ASDU. Required because `asdu.MeasuredValueFloatCP56Time2a` accepts only
  `COT` Spontaneous/Request and rejects the `Periodic`/`InterrogatedByStation`
  causes this outstation uses; for the same reason `confirmBitsString32Cmd` is
  hand-built (`asdu.BitsString32Cmd` accepts only `Activation`/`Deactivation`).

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
| `OnASDU` | everything else: monitoring data (single / double / measured-normal / measured-float — including the CP56-tagged `M_ME_TF_1`, logged with its type id — / integrated totals) and command confirmations via `onCommandReply` (`C_TS_TA_1`, `C_SC_*`, `C_DC_*`, `C_SE_*`, `C_BO_*`) |

Every one of these callbacks also calls `m.bridge.publishEvent(ca, ioa, typeId,
cot, value)` (nil-safe, so it's a no-op when no bridge is wired up, i.e. with `-demo`) —
see §5 for what that feeds.

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
   └─ -demo set?  → sendDemoCommands()  — see §2.2
      otherwise  → serve dispatch requests from the NestJS backend on stdin — see §5
   │
   ├─ server background reporting running (see §2.3)
   ▼
run until -duration elapses, Ctrl+C / SIGTERM or (bridge mode) stdin EOF
   ▼
client Close()  /  server Stop()
```

### 2.2 Demo command sequence (client → server)

Opt-in via the `-demo` flag (default off); with `-demo` unset the client instead
waits idle for `dispatch` requests on stdin (§5). When enabled, fired once STARTDT is
confirmed, in this order:

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
| 10 | Bitstring-32, time-tagged | `SendCmd(ca, C_BO_TA_1, 4000, 0x00FF00FF)` | IOA 4000 — sent, but dropped by the peer's decoder (Bug 3, §4) |
| 11 | Bitstring-32, untagged | `SendCmd(ca, C_BO_NA_1, 4001, 0x0F0F0F0F)` | IOA 4001 — round-trip verified |

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

The interrogated data is `M_SP_NA_1` (IOA 100), `M_DP_NA_1` (200),
`M_ME_NA_1` (300), `M_ME_NC_1` (301) and the CP56-tagged `M_ME_TF_1` (302), all
with `COT=20`.

**Counter interrogation** — same three-step pattern:
`C_CI_NA_1 Activation` → `ActivationCon` → `M_IT_NA_1` (IOA 400, COT 37) →
`ActivationTerm`.

**All other commands** — request `COT=Activation(6)`, then one fresh
`ActivationCon(7)` confirmation echoing the request parameters
(`C_CS_NA_1` echoes the CP56Time2a; `C_TS_TA_1` echoes the FBP test word +
time; `C_CD_NA_1` echoes the CP16Time2a; `C_BO_NA_1`/`C_BO_TA_1` echo the 32-bit
bitstring, the `TA` variant plus its CP56Time2a). A **read** command is answered with
the current value as `M_SP_NA_1 COT=Request(5)`.

**Background reporting** (per session, after the first GI):

```text
periodicLoop    : every -period      → M_ME_NC_1  IOA 301  COT=Periodic(1)     value += 12.5
spontaneousLoop : every -spontaneous → M_SP_TB_1  IOA 100  COT=Spontaneous(3)  toggle bool
spontaneousLoop : every -spontaneous → M_ME_TF_1  IOA 302  COT=Spontaneous(3)  analog (CP56-tagged)
```

IOA 302 repeats the analog value the periodic loop last reported (the spontaneous
loop only reads the process image), and IOA 301 wraps to -30 as soon as
`value += 12.5` would exceed 220 — so negative values in a longer run are expected,
not a fault (40 s run on 2026-09-25: 13 periodic + 19/19 spontaneous frames,
100.000 → 212.500 → -30.000, see `iec_60870_5_104_bug_report.md` §15).

### 2.4 Build & run

```powershell
# build both — run each module's build from inside that module's folder
cd iec104_server; go build ./...      # writes .\iec104_server\iec104-server.exe
cd ..\iec104_client; go build ./...   # writes .\iec104_client\iec104-client.exe

# terminal 1 — outstation (runs until Ctrl+C, or pass -duration)
.\iec104_server\iec104-server.exe -host 0.0.0.0 -port 2404 -duration 60s

# terminal 2 — master (bridge mode by default: reads NDJSON dispatch requests on
# stdin, writes ready/ack/event lines to stdout, logs to stderr; stdin EOF exits)
.\iec104_client\iec104-client.exe -host 127.0.0.1 -port 2404 -duration 40s

# -demo reinstates the old always-on command sequence (§2.2), useful without a
# backend attached
.\iec104_client\iec104-client.exe -host 127.0.0.1 -port 2404 -duration 40s -demo

# add -verbose to either program to print the raw cs104 frame/decode log;
# that is how the silent C_BO_TA_1 drop (Bug 3, §4) becomes visible
.\iec104_client\iec104-client.exe -host 127.0.0.1 -port 2404 -duration 40s -verbose

# print the build/VCS revision the binary was built from (revision=…, plus
# bridge=ndjson/<n>); an old gRPC-only client has no -version flag at all
.\iec104_client\iec104-client.exe -version
```

On the Raspberry Pi CM4/CM5 (Debian arm64) the same sources are pulled, built and
installed as systemd services from the repo root (full walk-through in the root
[`README.md`](../README.md)):

```bash
./pull.sh                  # git fetch + fast-forward (PAT from ~/.iec104-git-token)
./build.sh                 # dist/iec104-{server,client}, static linux/arm64, CGO off
sudo ./deploy/install.sh   # /opt/iec104 + systemd enable --now
```

Both programs' `-duration` defaults to `0` (= run until Ctrl+C/SIGTERM), so the
systemd units need no timer to stay up.

The two binaries are named after the module paths
(`github.com/alfas/iec104-server` / `-client`), not after the folder names
(`iec104_server` / `iec104_client`), which is why `go build ./...` inside each
folder yields exactly the names used above. A plain `cd iec104_client` after the
first command fails — it is relative to `iec104_server` — so use
`cd ..\iec104_client` as shown. When capturing a run to files, redirect with
`Start-Process -RedirectStandardOutput/-RedirectStandardError` (or `cmd /c ... 1> x
2> y`); PowerShell 5.1's `2>` wraps the native client's stderr in a
`NativeCommandError` record and writes UTF-16.

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
| `C_BO_NA_1` | Bitstring of 32 bits, command | Activation(6) | `station.OnASDU` — round-trip verified |
| `C_BO_TA_1` | Bitstring of 32 bits, command, with CP56Time2a | Activation(6) | `station.OnASDU` — handled here, but the frame never reaches the handler on the unpatched library (Bug 3, §4) |

### 3.2 Monitoring direction (outstation → master)

| Type ID | Name | COT(s) | Produced by |
| :-- | :-- | :-- | :-- |
| `M_SP_NA_1` | Single point | InterrogatedByStation(20), Request(5) | `sendAllData`, `OnRead` |
| `M_SP_TB_1` | Single point + CP56Time2a | Spontaneous(3) | `spontaneousLoop` |
| `M_DP_NA_1` | Double point | InterrogatedByStation(20) | `sendAllData` |
| `M_ME_NA_1` | Measured value, normalized | InterrogatedByStation(20) | `sendAllData` |
| `M_ME_NC_1` | Measured value, short float | InterrogatedByStation(20), Periodic(1) | `sendAllData`, `periodicLoop` |
| `M_ME_TF_1` | Measured value, short float + CP56Time2a | InterrogatedByStation(20), Spontaneous(3) | `sendAllData` (hand-built), `spontaneousLoop` (library helper) — IOA 302 |
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

### Bug 3 — `infoObjSize` has no entry for the time-tagged command types

Found while adding `C_BO_TA_1`. `asdu.infoObjSize` (go-iecp5
`asdu/identifier.go`) maps a type identification to its serial info-object size
and is consulted by **every** `UnmarshalBinary` (via `fixInfoObjSize`). It
contains the untagged `C_*_NA_1` commands but **none** of
`C_SC_TA_1`, `C_DC_TA_1`, `C_RC_TA_1`, `C_SE_TA_1`, `C_SE_TB_1`, `C_SE_TC_1`,
`C_BO_TA_1` (`C_TS_TA_1` is the only registered `TA` type), so a received frame
of those types is dropped before any handler runs:

```text
cs104 server => [E]: asdu UnmarshalBinary failed,asdu: type identification unknown
```

The error is invisible by default: `clog` starts disabled, and
`sf.Error()`/`sf.Warn()` print nothing until `LogMode(true)` — hence the
`-verbose` flag in both programs. Verified A/B with the same client session:
`C_BO_NA_1` (registered) is decoded → `[server] bitstring32 command: IOA=4001
value=0x0F0F0F0F` → `ActivationCon` confirmation received, while `C_BO_TA_1`
(not registered) produces the log line above and no handler call. The sender
never sees an error. Re-verified on 2026-09-25 with the same A/B in one session
(`logs/srv9.out` vs. `logs/srv8.out`; a run without `-verbose` shows no trace of
the dropped frame at all) — see `iec_60870_5_104_bug_report.md` §15.
`infoObjSize` is unexported and there is no registration
API, so there is **no application-level workaround** — only a patched library
(or the untagged `C_BO_NA_1`, which this repo uses by default) makes
`C_BO_TA_1` complete a round-trip. See the bug report §14.

See `iec_60870_5_104_bug_report.md` for the full analysis, the list of
verified-working functions, and the updated status table.

## 5. Stdio bridge (NDJSON)

The MSL5 NestJS backend and `iec104_client` run on the same Raspberry Pi, so
they talk over the child process's **stdin/stdout using newline-delimited JSON
(NDJSON)** instead of a network RPC. The backend (`apps/backend/src/iec104/bridge/`)
spawns the binary (`IEC104_BRIDGE_BIN`, arguments in `IEC104_BRIDGE_ARGS`) and
restarts it with exponential backoff if it exits. The authoritative wire
protocol is [`IEC104_GO_BRIDGE.md`](../IEC104_GO_BRIDGE.md); in short:

| Channel | Content |
| :--- | :--- |
| stdin | `{"op":"dispatch","reqId":"7","type":"OUT","points":[…]}` requests from the backend; **EOF = clean shutdown** (exit 0) |
| stdout | **protocol only**: `{"op":"ready","version":1}` once at startup, one `{"op":"ack","reqId":…,"accepted":…,"message":…}` per `dispatch`, and asynchronous `{"op":"event","point":{…}}` frames |
| stderr | all logs and diagnostics (the backend forwards them with a `[bridge]` prefix) |

`point` is the JSON form of the backend's `Iec104Dto`: an opaque
`id`/`designation`/`channelId` the backend uses to correlate frames, the
wire-format `asduTyp` mnemonic (e.g. `"C_SC_NA_1"`), the Common Address and
Information Object Address split into big-endian bytes (`asduHigh/asduLow`,
`ioaHigh/ioaMiddle/ioaLow` — IEC 104's 3-octet IOA has no single Go integer
equivalent, hence the split; `null` = not set), and a real JSON `payload`
object for the command value (`{"value":…}`) / decoded measurement
(`{"value":…,"cot":…}`).

**Outbound (`dispatch`):** for each point in the request,
`bridge.dispatchPoint` resolves `asduTyp` to an `asdu.TypeID` (`typemap.go`),
reassembles CA/IOA from the byte fields, registers the point under `(CA, IOA)`
for later correlation, and issues the matching `Send*Cmd` / `SendCmd` call on
the shared `*client.Client` — the same client the `-demo` path uses. Per-point
errors are collected into one `accepted:false` ack instead of failing the whole
batch; the ack means "accepted/queued", not "the outstation answered". Requests
are read with `bufio.Reader.ReadBytes('\n')` (not `bufio.Scanner`), so large
batches are never truncated.

**Inbound (`event`):** every `masterCall` callback (§1.6) calls
`bridge.publishEvent(ca, ioa, typeId, cot, value)`, which builds a `point`
(looking up the registered `id`/metadata for that `(CA, IOA)` if one was
dispatched earlier; otherwise `type` is inferred: `M_*` → `IN`, `C_*` → `OUT`)
and sends it into one buffered channel drained by a **single writer goroutine
that owns stdout**, so lines never interleave. The send is non-blocking because
`masterCall`'s callbacks all run on the underlying library's single
`handlerLoop` goroutine (see `iec104_client_architecture.md` §2.3) — a slow
stdout reader must never stall IEC 104 protocol processing; when the buffer is
full the frame is dropped and a warning is logged to stderr.

**stdout hygiene:** `main` points `os.Stdout` and the standard logger at stderr
and keeps the real stdout only for the bridge writer, and the go-iecp5 library
logger (which would default to stdout) is given a stderr `LogProvider`. A stray
print therefore cannot corrupt the protocol stream.

See [`iec104_client_architecture.md`](iec104_client_architecture.md) §7 for the
file-by-file breakdown and concurrency notes.