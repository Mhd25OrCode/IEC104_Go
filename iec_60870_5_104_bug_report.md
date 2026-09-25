# IEC 60870-5-104 — Verified Bugs and Working Functions

## 1. Summary

During testing of `wendy512/iec104` / `go-iecp5`, two separate issues were identified:

### Bug 1 — `SendReplyMirror()` can produce corrupted confirmations
The important sequence is:
```text
Get*Cmd()
    ↓
internal infoObj is consumed/advanced
    ↓
SendReplyMirror()
    ↓
incomplete ASDU
    ↓
client may report EOF / UnmarshalBinary failed
```
This is a real problem with the way the received ASDU is handled.

### Bug 2 — General Interrogation (`C_IC_NA_1`) — resolved in this repository
General Interrogation reaches the server, and the expected sequence:
```text
ActivationCon
    ↓
interrogated data
    ↓
ActivationTerm
```
was not observed by the client with the library's bundled example. It **is**
observed end-to-end by this repository's reference system, which answers the GI with
hands-built `ActivationCon` / `ActivationTerm` ASDUs (see §6, §13, §15). This is a
separate issue from the `SendReplyMirror()` problem.

### Bug 3 — `asdu.infoObjSize` is missing entries for Type IDs 58–64 (upstream `go-iecp5`)
The upstream ASDU codec (`github.com/wendy512/go-iecp5`, `asdu/identifier.go`)
defines Type IDs 58–64 (`C_SC_TA_1` … `C_BO_TA_1`) but omits all seven from its
`infoObjSize` table. `UnmarshalBinary` relies on `GetInfoObjSize()` to determine
how much data to consume for an Information Object, so every frame of those types
fails to decode (`ErrTypeIdentifier`, "asdu: type identification unknown") before
any handler runs. Still present in `v1.2.6` — see §14.

---

## 2. Bug #1 — `SendReplyMirror()` + `Get*Cmd()`

The main problem is that functions such as:
* `GetSingleCmd()`
* `GetInterrogationCmd()`
* `GetCounterInterrogationCmd()`

do not behave like completely side-effect-free getters. They consume/advance the internal `infoObj` data.

For example:
```go
cmd := pack.GetSingleCmd()
```
can leave the internal ASDU information-object buffer partially consumed. If the application then does:
```go
pack.SendReplyMirror(...)
```
the mirror is created from the remaining data rather than the original complete command. The resulting response can therefore be truncated.

The client may see:
```text
asdu UnmarshalBinary failed, EOF
```
because the received ASDU does not contain all of the expected information-object bytes.

---

## 3. Why this can happen even if the application does not call `Get*Cmd()`

This is particularly important for the server implementation. The library's internal dispatcher can parse the command before calling the application handler.

Conceptually:
```text
Incoming ASDU
      ↓
server dispatcher
      ↓
GetInterrogationCmd()
      ↓
application handler
      ↓
SendReplyMirror()
      ↓
corrupted confirmation
```

Therefore, this code is **not** necessarily safe:
```go
func onInterrogation(pack *asdu.ASDU) {
    // I didn't call GetInterrogationCmd() explicitly here, 
    // but the dispatcher might have.
    pack.SendReplyMirror(...)
}
```
The ASDU may already have been consumed by the library before `onInterrogation()` is called.

---

## 4. Correct Approach

Do not use `SendReplyMirror()` for these confirmations when the ASDU has been parsed with a `Get*Cmd()` operation.

Instead:
```text
Receive request
      ↓
Extract required command parameters
      ↓
Create a NEW ASDU
      ↓
Set Cause = ActivationCon
      ↓
Send new ASDU
```

For example, for General Interrogation, construct a new interrogation command/ASDU using the dedicated library helper rather than mirroring the received ASDU. The same principle applies to other command confirmations.

---

## 5. Verified Working Functions

The following functionality was successfully tested. Every entry below was
re-verified end-to-end on 2026-09-25 with the reference programs in this repository
(`iec104_server/` + `iec104_client/`) — see §15 for the exact run, the frame counts
and the log excerpts.

### Counter Interrogation
* **Status:** WORKING
* **Path:** Client $\rightarrow$ Counter Interrogation $\rightarrow$ Server $\rightarrow$ Response $\rightarrow$ Client.
* *Note:* Use the dedicated Counter Interrogation API rather than `SendReplyMirror()` on a consumed request.

### Read Command
* **Status:** WORKING
* *Note:* The request reaches the server and the response is processed correctly by the client.

### Clock Synchronization
* **Status:** WORKING
* *Note:* Clock synchronization was successfully tested end-to-end with no failures related to `SendReplyMirror()`.

### Test Command
* **Status:** WORKING
* *Note:* The Test Command confirmation works correctly, demonstrating that transport and confirmation mechanisms work when the problematic getter-mirror sequence is avoided.

### Single Command
* **Status:** WORKING
* **Execution Flow:**
  ```text
  Single Command
        ↓
  Server applies command       ✓
        ↓
  Device state changes         ✓
        ↓
  Spontaneous update           ✓
  ```
* *Note:* The confirmation should be constructed as a new ASDU, not by mirroring a consumed request.

### Double Command
* **Status:** WORKING
* *Note:* Command processing and path were successfully tested.

### Set-point Command (`C_SE_NC_1`)
* **Status:** WORKING
* *Note:* Short-float set-point activation (IOA 3000, 125.5) reaches the server and the echoed `ActivationCon` value is identical on the client.

### Bitstring-32 Command (`C_BO_NA_1`, `C_BO_TA_1`)
* **Status:** `C_BO_NA_1` WORKING; `C_BO_TA_1` blocked upstream (§14)
* *Note:* The untagged `C_BO_NA_1` (IOA 4001, `0x0F0F0F0F`) completes the full round-trip. The time-tagged `C_BO_TA_1` is sent and handled by the outstation code, but its frame is rejected inside the peer's `UnmarshalBinary` before any handler runs.

### Periodic Data
* **Status:** WORKING
* *Examples tested:* Temperature, pressure, energy/counter values, door sensor, alarm/state information.
* *In this reference system:* `M_ME_NC_1` IOA 301, COT Periodic(1), +12.5 per period, wraps from > 220 to -30 (so negative values in a long run are expected).

### Spontaneous Data
* **Status:** WORKING
* *Note:* Spontaneous state/data updates work correctly (also verified after actuator commands).
* *In this reference system:* `M_SP_TB_1` IOA 100 (toggling) and `M_ME_TF_1` IOA 302 (CP56-tagged, carrying the same analog value as the periodic IOA 301), both COT Spontaneous(3).

---

## 6. General Interrogation (`C_IC_NA_1`) — Bug 2

* **Status:** ✅ RESOLVED / WORKING
* **How:** the outstation answers a GI with the standard three-step sequence
  (`ActivationCon(7)` → interrogated data with COT 20 → `ActivationTerm(10)`),
  built as fresh ASDUs — see §13 and the live-run evidence in §15.
* *Historical note (original observation, now fixed):* the sequence
  `ActivationCon` → interrogated data → `ActivationTerm` was not observed by the
  client with the library example; it is observed end-to-end by this repository's
  reference system, re-confirmed on 2026-09-25.

---

## 7. Important: These Are Two Different Bugs

Do not combine them into one problem:

* **Bug 1:** `Get*Cmd()` + `SendReplyMirror()` $\rightarrow$ truncated/corrupted ASDU. *(Workaround: Build a fresh ASDU for the confirmation).*
* **Bug 2:** `C_IC_NA_1` $\rightarrow$ server receives the request, but the expected confirmation/data sequence was missing. *(Diagnosed and resolved in this repository: the outstation hand-builds `ActivationCon` / `ActivationTerm` instead of mirroring the consumed request — see §6, §13 and §15).*

---

## 8. Function Status Table

| Function / Feature | Status |
| :--- | :--- |
| Counter Interrogation | ✅ Working |
| Read Command | ✅ Working |
| Clock Synchronization | ✅ Working |
| Test Command | ✅ Working |
| Single Command execution | ✅ Working |
| Double Command | ✅ Working |
| Set-point Command (`C_SE_NC_1`) confirmations | ✅ Working |
| Bitstring-32 Command (`C_BO_NA_1`) | ✅ Working |
| Bitstring-32 Command, time-tagged (`C_BO_TA_1`) | ⚠ Application code complete — blocked by the upstream `infoObjSize` defect (§14) |
| Periodic data | ✅ Working |
| Spontaneous data | ✅ Working |
| General Interrogation | ✅ Working (resolved in this repository — §6, §13, §15) |
| `SendReplyMirror()` after `Get*Cmd()` | ❌ Do not use |

*The ✅ rows above were re-verified end-to-end on 2026-09-25 with the reference
programs in this repository; see §15 for the run and its logs.*

---

## 9. Functions / Patterns to Use

Keep the functionality that has been verified:
* Counter Interrogation
* Read Command
* Clock Synchronization
* Test Command
* Single Command
* Double Command
* Periodic Data
* Spontaneous Data

### Recommended Pattern for Confirmations:
```go
// Read/extract the command
cmd := pack.GetSingleCmd()

// Do NOT:
// pack.SendReplyMirror(...)

// Instead:
// Construct a new confirmation ASDU from cmd
```
For interrogation-style confirmations, use dedicated command constructors with `Cause = ActivationCon` rather than mirroring.

---

## 10. Functions / Patterns Not to Use

Avoid the following pattern:
```go
cmd := pack.GetSingleCmd()
pack.SendReplyMirror(...)
```
And the equivalent pattern for:
* `GetInterrogationCmd()`
* `GetCounterInterrogationCmd()`
* `GetReadCmd()`
* `GetResetProcessCmd()`
* `GetDelayAcquireCommand()`

...followed by `SendReplyMirror(...)`. The reason is that `Get*Cmd()` operations may have already consumed part of the internal buffer.

---

## 11. Recommended Architecture

Use this request-handling model:
```text
                  CLIENT REQUEST
                        │
                        ▼
                  RECEIVE ASDU
                        │
                        ▼
                  PARSE COMMAND
                        │
                        ▼
              Extract required values
                        │
                        ▼
               CREATE NEW ASDU
                        │
                        ▼
                ActivationCon
                        │
                        ▼
                     SEND
```

### For General Interrogation (Intended Architecture):
```text
C_IC_NA_1 Activation
          │
          ▼
    Parse QOI / CA
          │
          ▼
C_IC_NA_1 ActivationCon
          │
          ▼
     Send data ASDUs
          │
          ▼
C_IC_NA_1 ActivationTerm
```
*Note:* The last sequence is the target behavior, but General Interrogation is currently unverified.

---

## 12. Final Implementation Rule

> **Treat an ASDU as consumed after calling `Get*Cmd()`. Do not mirror it afterward.** Use the received ASDU to extract information, then construct a new ASDU for the response.

* **Verified Working:** Counter Interrogation, Read Command, Clock Synchronization, Test Command, Single Command, Double Command, Set-point, Bitstring-32, Periodic Data, Spontaneous Data.
* **Resolved:** General Interrogation (§6 / §13 / §15).
* **Unresolved:** `C_BO_TA_1` round-trip — upstream dependency defect only (§14).
* **Avoid API Pattern:** `Get*Cmd()` $\rightarrow$ `SendReplyMirror()`.
---

## 13. Resolution (test system in this repository)

The `iec104_server/` + `iec104_client/` programs in this repository implement a
working reference system that resolves both bugs documented above.

### Bug 1 — resolved by never mirroring a consumed ASDU
The server parses requests with `pack.Get*Cmd()` only to *read* parameters and
then constructs **brand-new ASDUs** for every response
(`iec104_server/confirm.go`):

```go
u := asdu.NewASDU(conn.Params(), asdu.Identifier{
    Type:       asdu.C_SC_NA_1,
    Variable:   asdu.VariableStruct{IsSequence: false, Number: 1},
    Coa:        asdu.CauseOfTransmission{Cause: asdu.ActivationCon},
    CommonAddr: ca,
})
u.AppendInfoObjAddr(cmd.Ioa)
u.AppendBytes(value)
conn.Send(u)
```

This also works around the fact that the library's command constructors
(`InterrogationCmd`, `ClockSynchronizationCmd`, `CounterInterrogationCmd`,
`ResetProcessCmd`, `DelayAcquireCommand`, `SingleCmd`, ...) either *force*
`Cause = Activation` or reject `ActivationCon`, so they cannot emit a proper
confirmation.

### Bug 2 — General Interrogation now verified working
The correct three-step sequence is observed end-to-end:

```text
client: C_IC_NA_1  COT=Activation(6)   QOI=20
server: C_IC_NA_1  COT=ActivationCon(7) QOI=20
server: M_SP_NA_1 / M_DP_NA_1 / M_ME_NA_1 / M_ME_NC_1   COT=InterrogatedByStation(20)
server: C_IC_NA_1  COT=ActivationTerm(10)                QOI=20
```

Verified with the reference implementation in live logs:

```text
[client] interrogation reply: IOA=0 QOI=20 COT=COT<ActivationCon>
[client] single point:    IOA=100 value=true  ... COT=COT<InterrogatedByStation>
[client] double point:    IOA=200 value=2     ... COT=COT<InterrogatedByStation>
[client] measured normal: IOA=300 value=0.305 ... COT=COT<InterrogatedByStation>
[client] measured float:  IOA=301 value=100.0 ... COT=COT<InterrogatedByStation>
[client] interrogation reply: IOA=0 QOI=20 COT=COT<ActivationTerm>
```

### Status table (updated)

| Function / Feature | Status |
| :--- | :--- |
| General Interrogation (`ActivationCon` → data → `ActivationTerm`) | ✅ Working (this repo) |
| Counter Interrogation | ✅ Working |
| Read Command | ✅ Working |
| Clock Synchronization | ✅ Working |
| Test Command | ✅ Working |
| Single / Double / Set-point Command confirmations | ✅ Working (as `ActivationCon`) |
| Periodic data | ✅ Working |
| Spontaneous data | ✅ Working |
| `SendReplyMirror()` after `Get*Cmd()` | ❌ Avoid (blank info body) |
| `C_BO_TA_1` (application level) | ⚠ Implemented — blocked by the upstream `infoObjSize` defect (Bug 3, §14) |

---

## 14. Bug 3 — `asdu.infoObjSize` is missing entries for Type IDs 58–64 (upstream defect in `github.com/wendy512/go-iecp5`)

### 14.1 Title

**`asdu.infoObjSize` has no entry for the time-tagged command type group
`C_SC_TA_1`, `C_DC_TA_1`, `C_RC_TA_1`, `C_SE_TA_1`, `C_SE_TB_1`, `C_SE_TC_1`,
`C_BO_TA_1` (Type IDs 58–64), so frames of those types cannot be unmarshaled by
`ASDU.UnmarshalBinary`.**

### 14.2 Description

Found while adding `C_BO_TA_1` to this repository; confirmed to affect the whole
time-tagged command group 58–64.

`asdu.infoObjSize` (`go-iecp5/asdu/identifier.go`) is an unexported
`map[TypeID]int` that maps a type identification to the serial octet size of one
information object. `ASDU.UnmarshalBinary` relies on it (via `fixInfoObjSize()` →
`GetInfoObjSize()`) to determine how much data to consume for an Information
Object, so **every** received ASDU of an affected type depends on a table entry:

```go
// go-iecp5/asdu/identifier.go
var infoObjSize = map[TypeID]int{
	...
	C_SC_NA_1: 1,
	C_DC_NA_1: 1,
	C_RC_NA_1: 1,
	C_SE_NA_1: 3,
	C_SE_NB_1: 3,
	C_SE_NC_1: 5,
	C_BO_NA_1: 4,     // only the UNTAGGED (NA) command variants are registered
	...
	C_TS_TA_1: 9,     // the only registered TA-tagged type
}

func GetInfoObjSize(id TypeID) (int, error) {
	size, exists := infoObjSize[id]
	if !exists {
		return 0, ErrTypeIdentifier // "asdu: type identification unknown"
	}
	return size, nil
}
```

The library defines the following seven Type IDs, and **none** of them has an
entry in `infoObjSize`:

| Type ID | Value | Information-object layout | Correct size (octets) |
| :--- | :--- | :--- | :--- |
| `C_SC_TA_1` | 58 | SCO (1) + CP56Time2a (7) | 8 |
| `C_DC_TA_1` | 59 | DCO (1) + CP56Time2a (7) | 8 |
| `C_RC_TA_1` | 60 | RCO (1) + CP56Time2a (7) | 8 |
| `C_SE_TA_1` | 61 | NVA (2) + QOS (1) + CP56Time2a (7) | 10 |
| `C_SE_TB_1` | 62 | SVA (2) + QOS (1) + CP56Time2a (7) | 10 |
| `C_SE_TC_1` | 63 | R32 (4) + QOS (1) + CP56Time2a (7) | 12 |
| `C_BO_TA_1` | 64 | 32-bit string (4) + CP56Time2a (7) | 11 |

`C_TS_TA_1` works only because it *is* registered (9 = 2 + 7). Adding the seven
missing rows is the only change required; the information-object codecs
(`AppendX`/`DecodeX`, `Get*Cmd()`) already exist and are correct.

### 14.3 Scope: the whole group 58–64, not one type

This is **not** a problem specific to `C_BO_TA_1`. All seven Type IDs 58–64 are
missing from `infoObjSize`, so the complete time-tagged command group in the
control direction is affected. These types exist everywhere else in the library
(constants, `AppendX`/`DecodeX` codecs, `Get*Cmd()` getters, and even
`client.SendCmd()`'s dispatch) but are absent from the size table.

### 14.4 Why `C_BO_TA_1` fails during `UnmarshalBinary`

`UnmarshalBinary` relies on `GetInfoObjSize()` to determine how much data to
consume for an Information Object. For `C_BO_TA_1` (Type ID 64) the lookup has no
entry and therefore returns `ErrTypeIdentifier`, which aborts the decode:

```text
C_BO_TA_1
    ↓
MarshalBinary
    ↓
Type-64 binary frame
    ↓
UnmarshalBinary
    ↓
fixInfoObjSize()
    ↓
GetInfoObjSize(64)
    ↓
ErrTypeIdentifier
    ↓
Round-trip fails
```

Upstream code path (`go-iecp5/asdu/asdu.go`):

```go
func (sf *ASDU) UnmarshalBinary(rawAsdu []byte) error {
	...
	sf.infoObj = append(sf.bootstrap[lenDUI:lenDUI], rawAsdu[lenDUI:]...)
	return sf.fixInfoObjSize() // -> GetInfoObjSize(sf.Type)
}

func (sf *ASDU) fixInfoObjSize() error {
	objSize, err := GetInfoObjSize(sf.Type)
	if err != nil {
		return err
	}
	...
}
```

### 14.5 Expected behavior

Every `TypeID` defined by the library — including `C_SC_TA_1` … `C_BO_TA_1`
(Type IDs 58–64) — should have a corresponding `infoObjSize` entry so that its
frames can be unmarshaled and delivered to the application handler like any other
type.

### 14.6 Actual behavior

For any Type ID missing from `infoObjSize`, `GetInfoObjSize()` returns
`ErrTypeIdentifier` ("asdu: type identification unknown"), `fixInfoObjSize()`
propagates it, and `UnmarshalBinary` fails. The receiver drops the frame inside
the cs104 read/dispatch loop **before** the application handler is called, and the
sender is not informed — `Send` returns `nil`:

```text
cs104 server => [E]: asdu UnmarshalBinary failed,asdu: type identification unknown
```

The failure is also silent by default: `clog.Clog` starts disabled, so
`sf.Error()`/`sf.Warn()` print nothing until `LogMode(true)` is called (this
repository exposes that switch as `-verbose` in both programs).

### 14.7 Version status: upgrading v1.2.5 → v1.2.6 does not resolve it

The defect is present in **both** `go-iecp5 v1.2.5` and `v1.2.6`. `v1.2.6` does
**not** add the missing entries; the same seven Type IDs (58–64) are still absent
from `infoObjSize`.

| Check | Result |
| :--- | :--- |
| Git tag `v1.2.5` → commit | `2269ee79a5e137847bbdc811be71a1064d303560` |
| Git tag `v1.2.6` → commit | `2269ee79a5e137847bbdc811be71a1064d303560` (**same commit**) |
| GitHub `compare/v1.2.5...v1.2.6` | `"status": "identical"`, `total_commits: 0`, `files: []` |
| `asdu/identifier.go` size | v1.2.5 = 21348 bytes; v1.2.6 = 21348 bytes |
| `asdu/identifier.go` SHA-256 (v1.2.5, module cache) | `0C6F5043D762E57539841E5A4E344CD460E36311FCC1EDF27936052D2887C6C3` |
| module proxy `@v/v1.2.6.info` | `Time: 2026-02-03T06:11:18Z`, `Hash: 2269ee79a5e137847bbdc811be71a1064d303560` |

Both tags resolve to the same commit, so the two versions ship an identical
`infoObjSize` table. Upgrading the dependency from `v1.2.5` to `v1.2.6` therefore
changes nothing for this defect.

### 14.8 Impact on `C_BO_TA_1` round-trip testing

`C_BO_TA_1` is implemented at the application level in this repository:

* client — `iec104_client/main.go` sends `SendCmd(ca, asdu.C_BO_TA_1, 4000, 0x00FF00FF)`;
* outstation — `iec104_server/handler.go` (`OnASDU`, case `C_BO_NA_1, C_BO_TA_1`)
  parses the request with `pack.GetBitsString32Cmd()`;
* confirmation — `iec104_server/confirm.go` (`confirmBitsString32Cmd`) builds the
  `ActivationCon` reply and appends the `CP56Time2a` for the `TA` variant.

The Type ID is recognized by the library and handled by the application, but the
frame is rejected during `UnmarshalBinary` at the receiver, so `C_BO_TA_1` cannot
complete a real `MarshalBinary → UnmarshalBinary` round-trip against an unmodified
published `go-iecp5 v1.2.6`. Re-verified on 2026-09-25 (§15): the type-64 frame
still disappears at the receiver while `C_BO_NA_1` completes its round-trip in the
same session.

**The application implementation should therefore not be marked as incomplete or
incorrect.** The blocker is in the upstream dependency's `infoObjSize` mapping.

Verified A/B evidence (same client session, two frames):

| Frame | Receiver behaviour |
| :--- | :--- |
| `C_BO_NA_1` (51, **registered**) | decoded → `[server] bitstring32 command: IOA=4001 value=0x0F0F0F0F COT=COT<Activation>` → client receives the `ActivationCon` confirmation |
| `C_BO_TA_1` (64, **not registered**) | `[E]: asdu UnmarshalBinary failed,asdu: type identification unknown` → dropped, no handler, no confirmation |

### 14.9 Current workaround / mitigation

There is **no application-level registration workaround**: `infoObjSize` is
unexported and `go-iecp5` does not expose a registration API for adding
Information Object sizes (the source comment only says "Type extensions must
register here"). The drop also happens inside the cs104 read/dispatch loop, before
any handler runs, so the application cannot intercept it. Mitigations:

1. **Patched copy of the library** — required if `C_BO_TA_1` must pass a real
   `MarshalBinary → UnmarshalBinary` round-trip. Add the seven rows (58–64) to
   `infoObjSize` and build against a patched copy, e.g.
   `replace github.com/wendy512/go-iecp5 => ./third_party/go-iecp5` in both
   modules, or `go mod vendor` + edit.
2. **Use the untagged twin `C_BO_NA_1` (51)** — registered and verified working
   end-to-end in this repository; the demo sends it alongside `C_BO_TA_1`.
3. **A custom `asdu.Params`/type ID is not an option**: the type byte on the wire
   must be 64 for a `C_BO_TA_1`, and the receiver validates it by that byte.

This repository keeps the complete `C_BO_TA_1` code path in place
(`confirmBitsString32Cmd`, the `OnASDU` case, the demo command), so the moment the
size table is patched the round-trip completes without any change here.

### 14.10 Reproduction / verification

All checks are read-only against the published source:

```powershell
# both tags resolve to the same commit
curl.exe -s https://api.github.com/repos/wendy512/go-iecp5/git/refs/tags/v1.2.5
curl.exe -s https://api.github.com/repos/wendy512/go-iecp5/git/refs/tags/v1.2.6

# "status": "identical", "total_commits": 0, "files": []
curl.exe -s "https://api.github.com/repos/wendy512/go-iecp5/compare/v1.2.5...v1.2.6"

# module metadata for v1.2.6 (same commit hash, same code)
curl.exe -s https://proxy.golang.org/github.com/wendy512/go-iecp5/@v/v1.2.6.info
```

On the wire, enable `-verbose` in either program to surface the otherwise silent
drop:

```text
cs104 server => [E]: asdu UnmarshalBinary failed,asdu: type identification unknown
```

### 14.11 Scope note

This section documents a defect in the upstream `go-iecp5` dependency as observed
against the published `v1.2.5`/`v1.2.6` code in this repository. It describes only
observable behavior of the library and makes no claims about the upstream
author's intent.

---

## 15. Live run evidence — 2026-09-25

Reference-system run used to re-verify every status in this report. Both programs
were rebuilt from the working tree (no source changes), then started exactly as
documented in `README.md`:

```powershell
cd iec104_server; go build ./...          # -> iec104_server\iec104-server.exe
cd ..\iec104_client; go build ./...       # -> iec104_client\iec104-client.exe

.\iec104_server\iec104-server.exe -host 0.0.0.0 -port 2404 -duration 60s    # logs\srv8.out / srv8.err
.\iec104_client\iec104-client.exe -host 127.0.0.1 -port 2404 -duration 40s  # logs\client8.out / client8.err
```

plus a short `-verbose` pair (`logs\srv9.out` / `logs\client9.out`) to make the
otherwise silent type-64 drop visible.

### 15.1 Result summary

| Check | Observed |
| :--- | :--- |
| Demo command sequence | 11/11 commands sent after the STARTDT confirmation |
| General Interrogation (§6, §13) | `ActivationCon(7)` → `M_SP_NA_1` (IOA 100), `M_DP_NA_1` (200), `M_ME_NA_1` (300), `M_ME_NC_1` (301), `M_ME_TF_1` (302), all COT 20 → `ActivationTerm(10)` |
| Counter Interrogation | `ActivationCon(7)` → `M_IT_NA_1` IOA 400 COT 37 → `ActivationTerm(10)` |
| Read | `M_SP_NA_1` IOA 100 with COT Request(5) |
| Clock sync / test / reset process | `ActivationCon(7)`; the CP56Time2a, the test word and QRP = 0x01 are echoed |
| Single / double / set-point float | `ActivationCon(7)`: IOA 1000 = true, IOA 2000 = 2, IOA 3000 = 125.500 |
| `C_BO_NA_1` (51) | decoded, `ActivationCon(7)` with IOA 4001 = `0x0F0F0F0F` |
| `C_BO_TA_1` (64) | dropped in `UnmarshalBinary`; no handler call, no confirmation, no error at the sender |
| Background reporting (40 s) | 13 × `M_ME_NC_1` IOA 301 COT 1; 19 × `M_SP_TB_1` IOA 100 and 19 × `M_ME_TF_1` IOA 302 COT 3 |
| Analog drift | IOA 301: 100.000 → … → 212.500 → **-30.000** (wrap) → -17.500 → -5.000 → 7.500; IOA 302 mirrors the same values |
| Lifecycle | client `StopDt` then `client stopped` after 40 s; `server stopped` after 60 s |

### 15.2 Verbatim excerpts

Master (`logs/client8.out`, 40 s session) — GI sequence, command confirmations and
the analog wrap:

```text
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

Outstation (`logs/srv8.out`, default, i.e. without `-verbose`) — the A/B pair; only
the untagged IOA 4001 appears, because the `C_BO_TA_1` sent to IOA 4000 produced
neither a handler line nor a confirmation:

```text
[server] bitstring32 command: IOA=4001 value=0x0F0F0F0F COT=COT<Activation>
```

Outstation (`logs/srv9.out`, `-verbose`) — the library log makes the drop visible in
the same session:

```text
cs104 server => [D]: RX Raw[68 18 12 00 00 00 40 01 06 00 01 00 a0 0f 00 ff 00 ff 00 ...]  <- C_BO_TA_1 (64)
cs104 server => [E]: asdu UnmarshalBinary failed,asdu: type identification unknown
cs104 server => [D]: RX Raw[68 11 14 00 00 00 33 01 06 00 01 00 a1 0f 00 0f 0f 0f 0f]     <- C_BO_NA_1 (51)
cs104 server => [D]: ASDU TID<C_BO_NA_1> COT<Activation> @1
[server] bitstring32 command: IOA=4001 value=0x0F0F0F0F COT=COT<Activation>
```

### 15.3 Notes for future runs

* The type-64 drop leaves **no trace at all** without `-verbose`: the non-verbose
  outstation log of this 60 s run is 716 bytes of pure application output.
* Each client session opens two TCP connections; the library's auto-connect closes
  the first one immediately. Expected, and present in every earlier run too.
* In PowerShell 5.1, `2>` on the native client binary wraps its stderr in a
  `NativeCommandError` record and writes the log as UTF-16. Use
  `Start-Process -RedirectStandardOutput/-RedirectStandardError` (or `cmd /c`) when
  capturing logs.
* `C_BO_TA_1` still cannot complete a round-trip; nothing in this repository needs
  to change once upstream adds the missing `infoObjSize` rows (§14.9).