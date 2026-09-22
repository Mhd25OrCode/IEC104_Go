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

### Bug 2 — General Interrogation (`C_IC_NA_1`) is still unresolved
General Interrogation reaches the server, but the expected sequence:
```text
ActivationCon
    ↓
interrogated data
    ↓
ActivationTerm
```
was not successfully observed by the client. This is a separate issue from the `SendReplyMirror()` problem.

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

The following functionality was successfully tested.

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

### Periodic Data
* **Status:** WORKING
* *Examples tested:* Temperature, pressure, energy/counter values, door sensor, alarm/state information.

### Spontaneous Data
* **Status:** WORKING
* *Note:* Spontaneous state/data updates work correctly (also verified after actuator commands).

---

## 6. General Interrogation (`C_IC_NA_1`)

* **Status:** `NOT WORKING / NOT YET RESOLVED` (Second Issue)

The client successfully sends the General Interrogation request:
```text
C_IC_NA_1 (Cause = Activation)
```
The server receives it, but the client does not receive the expected complete sequence:
```text
C_IC_NA_1 ActivationCon
        ↓
Interrogated data
        ↓
C_IC_NA_1 ActivationTerm
```

Current status summary:
```text
General Interrogation
        ↓
Request reaches server       ✓
        ↓
Expected response sequence   ✗
```

---

## 7. Important: These Are Two Different Bugs

Do not combine them into one problem:

* **Bug 1:** `Get*Cmd()` + `SendReplyMirror()` $\rightarrow$ truncated/corrupted ASDU. *(Workaround: Build a fresh ASDU for the confirmation).*
* **Bug 2:** `C_IC_NA_1` $\rightarrow$ server receives request, but expected confirmation/data sequence is missing. *(Remains unresolved; requires separate investigation).*

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
| Periodic data | ✅ Working |
| Spontaneous data | ✅ Working |
| General Interrogation | ❌ Not working / unresolved |
| `SendReplyMirror()` after `Get*Cmd()` | ❌ Do not use |

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

* **Verified Working:** Counter Interrogation, Read Command, Clock Synchronization, Test Command, Single Command, Double Command, Periodic Data, Spontaneous Data.
* **Unresolved:** General Interrogation.
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