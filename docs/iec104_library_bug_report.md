# go-iecp5 (IEC 60870-5-104) — Library Defects This Client Works Around

Verified against `github.com/wendy512/go-iecp5` **v1.2.5** and **v1.2.6** as used
by [`iec104_client/`](../iec104_client). Only the two defects that affect this
client are kept: a truncated confirmation (`SendReplyMirror` after `Get*Cmd()`)
and the missing `infoObjSize` rows for Type IDs 58–64.

> The earlier, longer version of this report also covered general/counter
> interrogation, read and clock sync on a two-program (outstation + master) test
> system. Those features are out of scope here — this client never answers GI, CI
> or read — so that material has been dropped. The analysis below is unchanged.

## 1. Bug 1 — `SendReplyMirror()` after `Get*Cmd()` sends a truncated confirmation

The getters are parsers, not side-effect-free accessors: they consume/advance the
ASDU's internal information-object buffer, so a mirror built afterwards is short.
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
the receiver reports EOF / UnmarshalBinary failed
```
Affected getters include `GetSingleCmd()`, `GetInterrogationCmd()`,
`GetCounterInterrogationCmd()`, `GetReadCmd()`, `GetResetProcessCmd()` and
`GetDelayAcquireCommand()`.

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

## 1.1 Why this can happen even if the handler does not call `Get*Cmd()`

This matters even without an explicit getter call: the library's internal
dispatcher can parse a command before invoking the application handler.

Conceptually:
```text
Incoming ASDU
      ↓
library dispatcher
      ↓
GetSingleCmd()
      ↓
application handler
      ↓
SendReplyMirror()
      ↓
corrupted confirmation
```

Therefore, this code is **not** necessarily safe:
```go
func onCommand(pack *asdu.ASDU) {
    // GetSingleCmd() was not called here, but the dispatcher might have.
    pack.SendReplyMirror(...)
}
```
The ASDU may already have been consumed by the library before `onCommand()` is called.

---

## 1.2 Correct approach

Do not use `SendReplyMirror()` for a confirmation when the ASDU has been parsed
with a `Get*Cmd()` operation.

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

Construct a new confirmation ASDU from the extracted parameters (with
`Cause = ActivationCon`) rather than mirroring the received one. The same
principle applies to every command confirmation.

---

## 2. Bug 3 — `asdu.infoObjSize` is missing entries for Type IDs 58–64

Upstream defect in `github.com/wendy512/go-iecp5`, present in v1.2.5 and v1.2.6.
All seven time-tagged command types are defined by the library but absent from
the size table used by `UnmarshalBinary`, so their frames are dropped before any
handler runs.

### 2.1 Summary

**`asdu.infoObjSize` has no entry for the time-tagged command type group
`C_SC_TA_1`, `C_DC_TA_1`, `C_RC_TA_1`, `C_SE_TA_1`, `C_SE_TB_1`, `C_SE_TC_1`,
`C_BO_TA_1` (Type IDs 58–64), so frames of those types cannot be unmarshaled by
`ASDU.UnmarshalBinary`.**

### 2.2 Description

Found while adding `C_BO_TA_1` support; confirmed to affect the whole time-tagged
command group 58–64.

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

### 2.3 Scope: the whole group 58–64, not one type

This is **not** a problem specific to `C_BO_TA_1`. All seven Type IDs 58–64 are
missing from `infoObjSize`, so the complete time-tagged command group in the
control direction is affected. These types exist everywhere else in the library
(constants, `AppendX`/`DecodeX` codecs, `Get*Cmd()` getters, and even
`client.SendCmd()`'s dispatch) but are absent from the size table.

### 2.4 Why `C_BO_TA_1` fails during `UnmarshalBinary`

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

### 2.5 Expected behavior

Every `TypeID` defined by the library — including `C_SC_TA_1` … `C_BO_TA_1`
(Type IDs 58–64) — should have a corresponding `infoObjSize` entry so that its
frames can be unmarshaled and delivered to the application handler like any other
type.

### 2.6 Actual behavior

For any Type ID missing from `infoObjSize`, `GetInfoObjSize()` returns
`ErrTypeIdentifier` ("asdu: type identification unknown"), `fixInfoObjSize()`
propagates it, and `UnmarshalBinary` fails. The receiver drops the frame inside
the cs104 read/dispatch loop **before** the application handler is called, and the
sender is not informed — `Send` returns `nil`:

```text
cs104 server => [E]: asdu UnmarshalBinary failed,asdu: type identification unknown
```

The failure is also silent by default: `clog.Clog` starts disabled, so
`sf.Error()`/`sf.Warn()` print nothing until `LogMode(true)` is called (the
client exposes that switch as `-verbose`).

### 2.7 Version status: upgrading v1.2.5 → v1.2.6 does not resolve it

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

### 2.8 Impact on this client

MSL5 never sends the time-tagged command types (58–64), so the defect does not
affect normal operation: this client only receives — and answers — the untagged
types listed in the MSL5 contract.

The tagged branches stay in `OnASDU` (`case C_SC_NA_1, C_SC_TA_1:` … and
`confirmBitsString32Cmd`, which appends the `CP56Time2a`) so a patched library
would work here without further changes. Against an unmodified published
`go-iecp5`, a type-64 frame disappears at the receiver (no handler call, no
confirmation, no error at the sender), while `C_BO_NA_1` completes its round-trip
in the same session.

**The omission is upstream, not in this client's application code.**

### 2.9 Workaround / mitigation

There is **no application-level registration workaround**: `infoObjSize` is
unexported and `go-iecp5` does not expose a registration API for adding
Information Object sizes (the source comment only says "Type extensions must
register here"). The drop also happens inside the cs104 read/dispatch loop, before
any handler runs, so the application cannot intercept it.

1. **Patched copy of the library** — add the seven rows (58–64) to `infoObjSize`
   and build against a patched copy, e.g.
   `replace github.com/wendy512/go-iecp5 => ./third_party/go-iecp5` in
   `iec104_client/go.mod`, or `go mod vendor` + edit.
2. **Use the untagged twin `C_BO_NA_1` (51)** — registered and verified working;
   it is the type MSL5 actually sends.
3. **A custom `asdu.Params`/type ID is not an option**: the type byte on the wire
   must be 64 for a `C_BO_TA_1`, and the receiver validates it by that byte.

No mitigation is needed today: MSL5 never sends 58–64.

### 2.10 Reproduction / verification

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

On the wire, enable `-verbose` in the client to surface the otherwise silent drop:

```text
cs104 server => [E]: asdu UnmarshalBinary failed,asdu: type identification unknown
```

### 2.11 Scope note

This section documents a defect in the upstream `go-iecp5` dependency as observed
against the published `v1.2.5`/`v1.2.6` code used by this repository. It describes
only observable behavior of the library and makes no claims about the upstream
author's intent.

