# IEC 60870-5-104 – Section „104-IN“ – Backend + Frontend Implementation Plan

**Status: planning only – no code was modified while writing this document.**

- Repository: `/home/alfas/MSL5`, branch `iec-104-new` (working tree inspected 2026-11-16).
- In scope: the frontend section **104-IN** and every MSL5 code path it touches
  (`src/apps/frontend/src/iec104/**`, `src/apps/backend/internal/device/iec104/**`,
  `src/packages/iec104/**`, `src/api/schemas/iec104.yaml`).
- **Scope change (2026-11-16, decided by the owner):** the Common Address is **removed from
  the Server tab and from the whole stack** (UI, API, DTO, ORM, validation). Both directions,
  **104-IN and 104-OUT, use the row's own CA bytes (`asduHigh`/`asduLow`)**. There is **no
  global/fallback CA**. The CA handling of 104-OUT (validation, `WritePoint`, OUT table
  labels/limits) is therefore in scope; the OUT *command semantics* (`C_*` write via
  `WritePoint`, `asduRetTyp`/`ioaRet*`) are not changed.

---

## 1. Target behaviour

1. „104-IN“ becomes the **command direction**: every active IN row is a `C_*`
   command that is sent to the outstation when the user presses **„Lesen“**.
2. Supported command types (**1:1 only**):
   `C_RD_NA_1` (102), `C_SC_NA_1` (45), `C_DC_NA_1` (46),
   `C_SE_NA_1` (48), `C_SE_NB_1` (49), `C_SE_NC_1` (50), `C_BO_NA_1` (51).
   Type IDs **58–64 are forbidden** (upstream `go-iecp5` Bug 3: no `infoObjSize`
   entry ⇒ inbound frames are silently dropped; this includes `C_SE_TC_1`/63,
   `C_SE_TA_1`/58, `C_SE_TB_1`/59, `C_BO_TA_1`/64).
3. General interrogation (`C_IC_NA_1`) and counter interrogation (`C_CI_NA_1`)
   are **deferred** (fan-out answers need an extra mapping concept).
4. The answer frames (M_* data or C_* confirmations) continue to be persisted
   unchanged into `iec104.responsePayload` and are shown in
   **„Antworten (Lesen)“**.
5. `asduHigh`/`asduLow` of a row (IN **and** OUT) are re-purposed as the **per-row Common
   Address** (`CA = asduHigh << 8 | asduLow`, both bytes **required**, 0..255, CA ≠ 0).
   `commonAddress` is **removed completely** (Server tab, API schema, DTO, ORM, DB model);
   no fallback exists anywhere.
6. `commandValue` of a row is derived from the **selected channel („Kanal“)**, not
   entered manually: digital channel → `{"value": true|false}`,
   analog channel → `{"value": <number>}`. Fallback when the channel has no
   usable value: `true` for digital commands, `1` for analog commands.
7. **No backward compatibility**: the legacy `M_* → C_RD_NA_1` conversion is removed.
   104-IN rows must use `C_*` types only. Existing legacy rows are deleted from the
   database instead of being migrated (see §3 D8 and §6).

---

## 2. Current state (verified against the working tree)

### 2.1 Frontend

| File | Current behaviour |
|---|---|
| `src/apps/frontend/src/iec104/models/iec104.ts:16-29` | `ASDU_TYPES_IN` mixes monitoring and command types: `M_SP_TB_1_3`, `M_SP_TB_1_30`, `M_ME_TF_1_36`, `C_SC_NA_1_45`, `C_SE_TC_1_63`, `C_BO_TA_1_64`. |
| `…/models/iec104.ts:35-53`, `103-119` | `Iec104InRow` / `createEmptyInRow` have no `commandValue`. |
| `…/components/Iec104InTable.tsx:63-74` | Headers: Kennzeichnung, Kanal, ASDU Typ, ASDU High, ASDU Low, IOA High, IOA Middle, IOA Low, Typ, Intervall, Übertragungsschwelle [%], Aktiv. |
| `…/components/Iec104InTable.tsx:109-152` | „ASDU Typ“ is a select limited to `ASDU_TYPES_IN`; „ASDU High“/„ASDU Low“ are free numeric inputs. |
| `…/components/Iec104InTable.tsx:257-272` | `ChannelSelectDialog.onSelect` stores only `channelId` + `channelName`; `ChannelDto.value` is discarded. |
| `…/services/iec104ApiService.ts:75-95` | `toInDto` sends `id/type/designation/channelId/channelName/asduTyp/asduHigh/asduLow/ioaHigh/ioaMiddle/ioaLow/typ/intervall/transmissionThreshold/active/createdAt` – **no `commandValue`**. |
| `…/services/iec104ApiService.ts:121-142` | `toInRow` ignores `dto.commandValue`. |
| `…/pages/Iec104InPage.tsx:68-87` | „Lesen“ = `readInRows(rows)`, then `setRows` + `setPayloadEntries` from the **202 body** (no follow-up refresh). |
| `…/services/iec104ChannelService.ts:7-23` | Input channel groups: `spi-digital-in`, `spi-analog-in`. |
| `…/pages/Iec104ServerPage.tsx:123-136` | „Common Address (CA)“ number input. |

`ChannelDto` (generated, `src/packages/api-node/src/generated.ts:1777-1783`) is
`{ channelUuid, name, path, protocol, value: number }` – the current channel value
is therefore already delivered by `GET /api/function/channels/groups`.

### 2.2 Backend (Go)

| File | Current behaviour |
|---|---|
| `src/apps/backend/internal/device/iec104/service/dispatch.go:14-29` | `dispatch(client, dir, ca, points)` uses **one** CA (from `Service.commonAddress()`) for the whole batch (**to be removed**). |
| `…/service/dispatch.go:32-79` | `commandFor`: for `dir == IN` a **monitoring** type is required (`IsMonitoringType`) and the command is hard-coded to `C_RD_NA_1` (`:68-78`). `asduTyp` only selects the point, never the command. |
| `…/service/address.go:26-40` | `ioaOf` requires all three bytes and a non-zero IOA. |
| `…/service/address.go:57-64` | `validIOA` (same rule) used by the save validation. |
| `…/service/cache.go:24-28`, `81-117`, `132-151` | Cache is keyed by **IOA only** (`byIOA map[uint32]*slot`); `Resolve(ioa, typeName)` ignores the CA; `ActiveIOAs()` returns bare IOAs. |
| `…/service/connection.go:64-69`, `:136`, `:163` | `commonAddress()` (from `settings_iec104`), a log field and `registerAll()` – **all to be removed**. |
| `…/service/server_settings.go:36-47`, `:66`; `orm/entities.go:62,69`; `orm/store.go:189,205`; `api/dto.go:152,165`; `internal/model/iec104.go:88` | `CommonAddress` plumbing through settings update/validation/store/DTO/GORM model – **to be removed**. |
| `…/service/dispatch.go:114` | `WritePoint` (OUT) uses `s.commonAddress()` – changes to the row CA. |
| `…/service/connection.go:160-171` | `registerAll()` registers `(ca from settings, ioa)`. |
| `…/service/frame.go:26-53` | Inbound frame → `Cache.Resolve(ioa, typeName)` → `store.UpdateResponsePayload`. CA is only used as a registry meta tag. |
| `…/service/service.go:129-207` | `validateBatch`: `asduTyp` non-empty, IOA three bytes/non-zero, `asduHigh`/`asduLow` 0..65535, duplicate key `type/ioa`. |
| `…/service/service.go:234-259` | `Read`: link check → diff-save → `store.List` → `dispatch` → HTTP 202 with the **pre-dispatch** rows. |
| `…/orm/store.go:141-154` | IN update nulls `asduRetTyp`/`ioaRet*`; `commandValue` is persisted for all point types. |
| `src/packages/iec104/cmd.go:10-18`, `33-52` | `valueCommandKinds` + `CommandFor` + `IsValueCommandType`; `dispatch` (`…/commander.go:23-49`) already maps every `C_*` (incl. `SendReadCmd`). |
| `src/api/schemas/iec104.yaml:1-8` | `Iec104Type` documented as „IN = Meldung von der Gegenstelle“ – becomes wrong. |
| `src/api/schemas/iec104.yaml:47-53` | `asduHigh` documented as „Nur Anzeige; die Typauflösung erfolgt über `asduTyp`“. |

### 2.3 Facts that drive the design

- `iec104.ReadCmd` in `go-iecp5` force-sets `COT = Request (5)`, so `C_RD_NA_1` stays valid.
- The point `commandValue` column and the whole backend plumbing already exist for
  both directions (`orm/store.go:84`, `:152-154`); only the frontend never sends it for IN.
- `src/packages/iec104/payload.go` maps every answer family (single/double/measured/
  counter/command info) to JSON, so persisted payloads need no change.

---

## 3. Decisions

| ID | Decision |
|---|---|
| D1 | „Lesen“ sends, for every **active** IN row, exactly the command stored in `asduTyp` (no implicit `C_RD_NA_1`). |
| D2 | Only `C_RD_NA_1`, `C_SC_NA_1`, `C_DC_NA_1`, `C_SE_NA_1/NB/NC`, `C_BO_NA_1` are offered. `C_SE_NA/NB` carry an **int16** on the wire (`value.go`), not a float. IDs 58–64 never. |
| D3 | `asduHigh`/`asduLow` = CA bytes of the row, **mandatory** for IN and OUT (0..255 each, CA ≠ 0). **No fallback, no global CA**; `commonAddress` is deleted everywhere (owner decision). |
| D4 | CA is resolved **per point** for IN and OUT. The TCP session itself is unaffected (IEC 104 carries the CA in every ASDU); the connection check no longer involves a CA. |
| D5 | `commandValue` is computed in the frontend from the selected channel's current `value` when „Lesen“ is pressed; no new UI column. |
| D6 | If the channel is missing/`NaN`/`undefined`: digital command → `true`, analog command → `1`. |
| D7 | „Lesen“ keeps HTTP 202 semantics, but the frontend refreshes `responsePayload` afterwards (short polling) so the answer of the same click becomes visible. |
| D8 | **No legacy fallback / no backward compatibility**: `M_*` types in 104-IN are invalid. The `M_* → C_RD_NA_1` branch is not implemented; existing IN rows with `M_*` are deleted from the DB and re-created as `C_*` rows. |

---

## 4. Required backend changes

### B1 – `src/packages/iec104/cmd.go` (helper)

Add an exported classifier so both the service and validation agree:

```go
// IsReadRequestCommand reports whether the mnemonic is C_RD_NA_1.
func IsReadRequestCommand(asduTyp string) bool
// ValueKindOf maps a command mnemonic to the wire value kind, e.g.
// "bool" (C_SC), "uint8" (C_DC), "int16" (C_SE_NA/NB), "float32" (C_SE_NC), "uint32" (C_BO).
// Must mirror iec104.CommandValue (value.go): C_SE_NA/NB take cast.ToInt16E, NOT float32.
type ValueKind int
const (ValueNone ValueKind = iota; ValueBool; ValueUint8; ValueInt16; ValueFloat32; ValueUint32)
func ValueKindOf(asduTyp string) ValueKind
```

`IsValueCommandType`/`valueCommandKinds` (`cmd.go:10-18`, `47-52`) already exist and
can back `ValueKindOf`. New `iec104/cmd_test.go` cases for all supported types and
for the forbidden 58–64 group (must be reported as unsupported).

### B2 – `src/apps/backend/internal/device/iec104/service/address.go`

Add the per-row CA resolver (CA bytes are 0..255, CA 0 is invalid for the library):

```go
func caOf(p orm.Point) (uint16, bool) {
    if p.AsduHigh == nil || p.AsduLow == nil { return 0, false }
    if *p.AsduHigh < 0 || *p.AsduHigh > 0xFF || *p.AsduLow < 0 || *p.AsduLow > 0xFF {
        return 0, false
    }
    ca := uint16(*p.AsduHigh)<<8 | uint16(*p.AsduLow)
    return ca, ca != 0
}
```

`ioaOf` (`address.go:26-40`) stays unchanged: all supported IN commands address a
single object and need a non-zero IOA.

### B3 – `…/service/dispatch.go` (core change)

Replace the IN branch of `commandFor` (`:68-78`):

```go
mnemonic, ok := mnemonicOf(p)                 // unchanged
ioa, ok := ioaOf(p)                           // unchanged
if dir == model.Iec104TypeIN {
    ca, ok := caOf(p); if !ok { log Info "CA invalid"; return skip }   // D3, no fallback
    switch iec104.ValueKindOf(mnemonic) {
    case iec104.ValueNone:                    // C_RD_NA_1 (and future C_IC/C_CI)
        return iec104.CommandFor(mnemonic, ca, ioa, nil, key)
    case iec104.ValueBool, iec104.ValueUint8, iec104.ValueInt16, iec104.ValueFloat32, iec104.ValueUint32:
        value, ok := commandValueOf(p)         // existing helper, address.go:42-55
        if !ok { log + skip with reason }
        return iec104.CommandFor(mnemonic, ca, ioa, value, key)
    }
}
```

Also:
- `dispatch(...)` no longer takes a `ca` parameter at all; `commandFor` resolves the CA per
  point (B2) for IN and OUT. `Service.Read` just calls `s.dispatch(client, dir, rows)`.
- `key := addrKey{CA: ca, IOA: ioa}` must use the **resolved row CA**
  so `Layer.Send` registers the right `(CA, IOA)` meta pair.
- Promote the skip reasons to `Info`: currently „unknown ASDU type“ is `Warn` and
  „not a monitoring type“ is `Debug` (`dispatch.go:36-78`) ⇒ invisible in
  `vigil logs`. New reasons: „IN point has no command type“, „commandValue missing“,
  „CA invalid“, „forbidden type id 58–64“.
- **No compatibility branch (D8).** A monitoring type (`M_*`) in an IN row is *not*
  converted to `C_RD_NA_1`. It is rejected by validation (B6); rows that still exist in
  the DB are skipped by `commandFor` with an `Info` log and the reason
  „IN point has no command type“. `IsMonitoringType` is no longer used by the IN branch.

`WritePoint` (`dispatch.go:81-119`, OUT): replace `ca := s.commonAddress()` by
`ca, ok := caOf(p)`; `!ok` ⇒ `Invalid("point %s: CA bytes are incomplete")`. Nothing else
in the OUT flow changes.

### B4 – `…/service/cache.go` – key by (CA, IOA)

With per-row CAs the IOA-only cache is ambiguous. Re-use the existing `addrKey`
(`address.go:10-13`).

**CA source:** `caOf(p)` for **every** row (IN and OUT) – there is no fallback CA, so the
cache needs no settings dependency and no rebuild on settings change. Rows without valid CA
bytes are not indexed (`indexed=false`, like rows without a valid IOA today). The IN/OUT
`slot` sharing per address stays; note the residual ambiguity when an IN and an OUT row use
the same `(CA, IOA, type)` – `Resolve` keeps the existing precedence (IN wins on equal
mnemonic); document it, do not redesign it.

- `byIOA map[uint32]*slot` → `byAddr map[addrKey]*slot`.
- `ActiveIOAs() []uint32` → `ActiveAddrs() []addrKey`.
- `Resolve(ioa uint32, typeName string)` → `Resolve(ca uint16, ioa uint32, typeName string)`.
- `add`/`detach`/`Load` adapted accordingly (`slot.in`/`slot.out` stay).

### B5 – `…/service/frame.go` and `…/service/connection.go`

- `frame.go:26-53`: resolve the CA from `ev.Meta.(addrKey)` when present, else from
  `ev.CA`; call `cache.Resolve(ca, ioa, ev.TypeName)`.
- `connection.go:160-171` `registerAll()`: iterate `ActiveAddrs()` instead of
  `ActiveIOAs()`; the `ca` lookup from settings disappears.
- `connection.go:64-69` `commonAddress()` is **deleted**, as is the `"ca"` log field at `:136`.
- Add an `Info` log when a frame hits no point (`ioa`, `ca`, `typeName`) – today this
  is `Debug` (`frame.go:33`), which is why missing payloads are hard to diagnose.

### B6 – `…/service/service.go` – validation

`validateBatch` (`:129-207`) must accept the new semantics for IN rows:

1. `asduTyp` must be a **command** type for IN: `C_*` from the supported list and not
   one of 58–64. Monitoring types (`M_*`) are rejected — there is no compatibility
   fallback (D8).
2. `asduHigh`/`asduLow`: **for IN and OUT rows**, both are **required**, each 0..255, and
   `(high<<8|low) != 0` (new `validCA`, analogous to `validIOA`). Today's check at
   `:159-163` (0..65535, optional) stays only for `ioaRet*`/`transmissionThreshold`.
3. Duplicate detection (`:178-183` and `:193-205`) must key on
   **`type/CA/IOA`** for both directions (the stored-row loop uses `caOf`; stored rows
   without valid CA bytes are skipped like rows without IOA), otherwise two rows with the same
   IOA at different CAs are wrongly rejected.
4. For value commands (`C_SC`, `C_DC`, `C_SE_*`, `C_BO`) `commandValue` is optional at
   save time (it is derived from the channel at „Lesen“ time); the dispatch path logs
   and skips when it is missing (`B3`).
5. Keep the `ioa` rules unchanged (`validIOA`, `address.go:57-64`).

`Service.Read` (`:234-259`) keeps the diff-save + `store.List` + `dispatch` order.
Optional hardening: return `204`/skip dispatch when no active IN point is left, so an
empty „Lesen“ does not look like a successful read.

### B7 – Contract & documentation

- `src/api/schemas/iec104.yaml:1-8`: `Iec104Type` description must state that IN is the
  **command direction** (`C_*` sent to the outstation) and OUT the remaining direction.
- `:47-53`: `asduHigh`/`asduLow` are no longer „nur Anzeige“ but the (required) CA bytes
  for IN and OUT, 0..255.
- **Remove `commonAddress`** from `Iec104ServerSettingsDto` (`:214-218`, required list
  `:225`) and `Iec104UpdateServerSettingsDto` (`:241-244`). This **is** a wire-shape change
  (breaking, intended): regenerate and fix `api/dto.go`, `packages/api-go/gen.gen.go`,
  `@msl5/api-node` and `api/openapi.bundled.yaml`.
- Regenerate `@msl5/api-node` / `@msl5/api-go` (`pnpm check:api`, `pnpm check:api-go`).
- `docs/architecture/api.md` (IEC 104 section) and `docs/architecture/frontend.md:210`
  („Lesen“ description **and** the Server-tab sentence that lists `commonAddress` as
  editable) must be updated; `docs/iec104-client-integration-plan.md`
  should get a short „Revision“ note pointing at this plan.

### B8 – Backend tests (new; there are none in `internal/device/iec104` today)

| Test file | Cases |
|---|---|
| `src/packages/iec104/cmd_test.go` | `ValueKindOf` for every supported type; 58–64 reported unsupported; `IsReadRequestCommand`. |
| `…/service/dispatch_test.go` | IN + `C_RD_NA_1` → one `SendReadCmd` equivalent; IN + `C_SC_NA_1` with/without `commandValue`; per-row CA (invalid/missing CA ⇒ skipped, no fallback); OUT `WritePoint` uses the row CA; inactive row sends nothing; IN + `M_*` sends **nothing** and logs the skip reason. |
| `…/service/validate_test.go` | CA bytes 0..255; duplicate IOA at different CA allowed; duplicate (CA, IOA) rejected; `M_*` in IN always rejected. |
| `…/service/cache_test.go` | same IOA at two CAs resolves to the correct point; `ActiveAddrs` de-duplicates. |

---

## 5. Required frontend changes

### F1 – `models/iec104.ts`

- `ASDU_TYPES_IN` → command-only list, with the numeric TypeID in the label:

```ts
export const ASDU_TYPES_IN = [
  { value: "C_RD_NA_1",  label: "C_RD_NA_1 [102] – Lesen" },
  { value: "C_SC_NA_1",  label: "C_SC_NA_1 [45] – Einzelbefehl" },
  { value: "C_DC_NA_1",  label: "C_DC_NA_1 [46] – Doppelbefehl" },
  { value: "C_SE_NA_1",  label: "C_SE_NA_1 [48] – Sollwert normiert" },
  { value: "C_SE_NB_1",  label: "C_SE_NB_1 [49] – Sollwert skaliert" },
  { value: "C_SE_NC_1",  label: "C_SE_NC_1 [50] – Sollwert float" },
  { value: "C_BO_NA_1",  label: "C_BO_NA_1 [51] – Bitstring 32" },
] as const;
```

  `ASDU_TYPES_OUT` and `ASDU_RET_TYPES_OUT` are **not** touched (only the OUT *CA* columns change, see F2b).
- `Iec104InRow` gains `commandValue?: { value: boolean | number } | null`.
- `createEmptyInRow` initialises `commandValue: null`; the `asduTyp` default stays `""`.
- Add a helper `isValueCommand(asduTyp): boolean` (mirrors `iec104.ValueKindOf`).

### F2 – `components/Iec104InTable.tsx`

- Relabel the two CA columns: **„CA High“** / **„CA Low“** (same `asduHigh`/`asduLow`
  fields, no migration) and constrain the inputs to `min={0} max={255}`.
- No new column for the command value (per requirement). Optionally show the derived
  value as read-only text in the „Kanal“ cell tooltip.
- Optional visual guard: when `row.asduTyp` is a value command but no channel is
  selected, mark the row (e.g. warning icon) – the value then falls back to the default.

### F2b – `components/Iec104OutTable.tsx`

Same relabel as F2: the OUT columns `asduHigh`/`asduLow` become **„CA High“/„CA Low“**,
`min=0 max=255`, required (empty ⇒ row marked invalid; save is rejected by B6). Update
`createEmptyOutRow` keeps the CA fields empty (no default); the user must enter the CA (D3).

### F3 – `services/iec104ApiService.ts`

- `toInDto` (`:75-95`): add `...(row.commandValue ? { commandValue: row.commandValue } : {})`.
- `toInRow` (`:121-142`): map `dto.commandValue as { value: boolean | number } | null`.
- New pure helper (unit-testable, no network):

```ts
function commandValueFor(row: Iec104InRow, channels: ChannelDto[]) {
  const kind = valueKindOf(row.asduTyp);           // "bool" | "uint8" | "int16" | "float32" | "uint32" | "none"
  if (kind === "none") return undefined;
  const ch = channels.find(c => c.channelUuid === row.channelId);
  const raw = ch?.value;
  const usable = typeof raw === "number" && Number.isFinite(raw);
  if (kind === "bool")  return { value: usable ? Boolean(raw) : true };
  if (kind === "uint8") return { value: usable ? Math.max(0, Math.min(3, Math.round(raw))) : 1 };
  if (kind === "int16")  return { value: usable ? Math.max(-32768, Math.min(32767, Math.round(raw))) : 1 };
  if (kind === "uint32") return { value: usable ? Math.max(0, Math.round(raw)) : 1 };
  return { value: usable ? raw : 1 };              // float32 (C_SE_NC only)
}
```

- `readInRows(rows)` → `readInRows(rows, channels)` or better: fetch the channel value
  inside `Iec104InPage` and pass an already-enriched row list (keeps the service free
  of UI state).

### F4 – `pages/Iec104InPage.tsx`

`handleLesen` (`:68-87`):

1. `const groups = await getIec104InputChannelGroups();` (already exists,
   `iec104ChannelService.ts:7-23`) → flatten to `ChannelDto[]`.
2. Map `rows` → enriched rows with `commandValue = commandValueFor(row, channels)`.
3. `const result = await iec104ApiService.readInRows(enriched)`.
4. `setRows(result)` + `setPayloadEntries(...)` (unchanged).
5. **Poll for the answer** (D7): `for (i < 3) { await sleep(700); const fresh =
   await iec104ApiService.getInRows(); merge responsePayload by id; }` and stop early
   when every active row has a payload. Without this the panel keeps showing
   „— kein Payload —“ for the click that just sent the command.

### F5 – `pages/Iec104ServerPage.tsx`

Remove the „Common Address (CA)“ block (`:123-136`) **and the field itself**: drop
`commonAddress` from `Iec104ServerSettings` and its default (`models/iec104.ts:84,99`), from
`toServerSettings` (`iec104ApiService.ts:174`) and `saveServerSettings` (`:336`), and from the
test fixtures (`iec104Api.test.ts:202-262`). The tab keeps `enabled`, `ipAddress`, `port` and
the read-only `state`. Nothing is shown read-only either (resolves OQ-4).

### F6 – Frontend tests

- `services/iec104Api.test.ts`: `readInRows` sends `commandValue`; `toInRow` restores it.
- New unit tests for `commandValueFor`: digital/analog channel, missing channel → `true`
  / `1`, `NaN` → default, `uint8` clamping (0..3), `int16` clamping.
- `models` test: `ASDU_TYPES_IN` contains no `M_*` and no TypeID 58–64.

---

## 6. Data & compatibility

- **No DB migration.** `asduHigh`/`asduLow` keep their columns, only their meaning
  changes („ASDU“ → CA bytes). `commandValue` already exists on the shared point table
  `iec104` (`model.Iec104Point.TableName()`, `internal/model/iec104.go:40`); IN and OUT
  rows live in the same table and are discriminated by the `type` column.
- **Existing IN rows with an `M_*` type are not migrated (D8).** There is no
  `M_* → C_RD_NA_1` fallback. Legacy rows must be deleted and re-created with `C_*`
  types, e.g. `DELETE FROM iec104 WHERE type = 'IN';` — only after a backup, because the
  table also holds the OUT rows. The backend neither converts nor auto-migrates them; it
  only logs the skip reason (B3).
- **`settings_iec104.commonAddress` is removed** from the GORM model
  (`internal/model/iec104.go:88`). `AutoMigrate` does not drop columns; the old
  `NOT NULL DEFAULT 1` column simply stays unused and inserts keep working. Optionally drop it
  with `ALTER TABLE settings_iec104 DROP COLUMN "commonAddress";` in the release notes.
- **Existing IN *and* OUT rows without valid CA bytes** (nil, or > 255, e.g. old OUT display
  values up to 65535) are skipped by dispatch (`Info` log „CA invalid“) and rejected on the next
  save. They must be edited (CA entered) or deleted before the feature is used. No auto-migration.
- CAs ≥ 1 only: CA 0 is `asdu.InvalidCommonAddr` in `go-iecp5`; the outstation answers
  with „unknown common address“.

## 7. Acceptance / test plan

| # | Scenario | Expected |
|---|---|---|
| T1 | IN row `C_RD_NA_1`, CA 1, IOA 100, Aktiv ✔, outstation running | after „Lesen“ the panel shows a `M_SP_NA_1` payload (COT 5) for that row |
| T2 | IN row `C_SC_NA_1`, digital channel, IOA 100 | outstation log `[server] single command: IOA=100 value=<ch>`, panel shows `C_SC_NA_1` ActivationCon payload |
| T3 | `C_SE_NC_1` with an analog channel value 42.5, IOA 301 | `[server] setpoint float command: IOA=301 value=42.5` |
| T4 | `C_BO_NA_1` with value 0x0000000F, IOA 400 | `[server] bitstring32 command`, echo payload |
| T5 | two rows, same IOA, CA 1 and CA 2 | save accepted, both dispatch with their own CA, payloads stored per (CA, IOA). The outstation echoes the request CA (change O3 implemented), so no CA has to be configured on it |
| T6 | value command, no channel selected | default `true`/`1` is sent (no error) |
| T7 | row with `C_SE_TC_1`/`C_BO_TA_1` | rejected by the dropdown; a stored legacy row is skipped with an `Info` log |
| T8 | link not active | HTTP 409 (unchanged, `service.IsNotConnected`) |
| T9 | `GET/PUT /api/iec104/server` | no `commonAddress` in request or response; Server tab shows only enabled/IP/port/state; a PUT with the legacy field is rejected/ignored per OpenAPI validation |
| T11 | IN or OUT row with empty CA bytes | save rejected (`ca must be two bytes 0..255 and not 0`); a stored legacy row is skipped with an `Info` log |
| T12 | OUT row `C_SC_NA_1`, CA 2 | `WritePoint` sends with CA 2 (outstation echo shows CA 2) |
| T10 | IN row with a legacy `M_*` type still in the DB | „Lesen“ sends nothing for it and logs the skip reason at `Info`; the row must be deleted (D8) |

Manual/regression: `pnpm --filter @msl5/frontend test`, `pnpm lint`, `go build ./...`
(`src/apps/backend`), `go test ./...` for the touched packages,
`pnpm check:api` / `pnpm check:api-go`.

## 8. Risks & limitations

1. **No fan-out**: `C_IC_NA_1`/`C_CI_NA_1` are not offered; their answers span IOAs
   100/200/300/301/302/400 and cannot be mapped onto one row.
2. **No payload for IOA-0 confirmations**: `C_TS_NA_1`, `C_TS_TA_1`, `C_RP_NA_1`,
   `C_CS_NA_1` confirmations carry `InfoObjAddrIrrelevant`/0 and cannot be stored
   (the cache has no IOA 0 entry). They are intentionally not in the dropdown.
3. **Stale payload without polling**: keep F4.5, otherwise the UX problem from the
   bug report returns.
4. **Type 58–64**: silently dropped by the decoder on the MSL5 side (upstream bug) –
   permanently excluded from the UI and rejected by validation.
5. **CA semantics** assume one CA per row for both directions (and the outstation must echo the request CA, see outstation plan O3); if a point ever needs
   different CAs for command and answer, an extra column is required (OQ-2).
6. **„Lesen“ now performs real control actions.** `C_SC`/`C_DC`/`C_SE`/`C_BO` change the
   outstation process. A save-with-diff + „Lesen“ click on a mis-configured row writes to
   the field device. Consider a confirmation dialog when any active row is a value command.
7. **Derived `commandValue` is a snapshot**: the channel value read at „Lesen“ time is
   what is sent, not a live link to the SPI channel.

## 9. Assumptions

- A1: The outstation expects the standard command forms (`C_SC` bool, `C_DC` 0..3,
  `C_SE_NA` normalized/scaled, `C_SE_NC` float32, `C_BO` uint32).
- A2: The selected channel is an SPI input (`spi-digital-in` / `spi-analog-in`), i.e.
  a process value that is suitable as a command value.
- A3: `ChannelDto.value` is the current raw value (digital: 0/1, analog: engineering
  value) and is already returned by `GET /api/function/channels/groups`.
- A4: The UI language stays German („Lesen“, „Speichern“, „Antworten (Lesen)“).
- A5: 104-OUT keeps its command semantics, but its CA now also comes from the row bytes; no CA is configured globally.
- A6: All legacy IN rows (`M_*`) are deleted before the feature is enabled; there is no
  compatibility path (D8).

## 10. Open questions

**Resolved in §3:** the former OQ-1 (`M_* → C_RD_NA_1` fallback) is **not implemented** –
no backward compatibility, legacy IN rows are deleted (D8).

- **OQ-2**: Should the CA ever differ between command and answer for one row
  (then the `asduRetTyp`/`ioaRet*` columns, currently nulled for IN in
  `orm/store.go:141-145`, would have to be re-enabled)?
- **OQ-3**: Is a 5 s poll after „Lesen“ acceptable, or should the backend wait for the
  frames and answer synchronously (200) instead?
**Resolved:** OQ-4 – the CA is removed completely (owner decision, no read-only display).

- **OQ-5**: Should „Lesen“ abort with an error when no active IN row exists?

## 11. Implementation order

Each step must leave `go build ./...` / `pnpm lint` green; commit per step.

1. **B1** `cmd.go` helpers + `cmd_test.go` (no behaviour change).
2. **B2 + B4 + B5** CA plumbing: `caOf`, cache keyed by `addrKey`, `frame.go`, `registerAll`.
   Tests first for the cache (same IOA at two CAs; IN+OUT slot sharing).
3. **B6** validation (IN-only rules) + `validate_test.go`.
4. **B3** `dispatch.go` IN branch + `WritePoint` row CA, log levels + `dispatch_test.go`.
4b. **Remove `commonAddress`**: delete `commonAddress()`, settings validation/update/store/DTO/GORM field (backend compiles again only when all call sites are gone – do this in one commit with B3).
5. **B7** `iec104.yaml` (descriptions **and** removal of `commonAddress`), `pnpm check:api` / `check:api-go`, architecture docs.
6. **F1–F3** model, service, `commandValueFor` + unit tests.
7. **F2/F2b/F4/F5** IN+OUT table labels, `handleLesen` (channels → commandValue → read → poll), Server page.
8. Outstation changes (separate doc/repo) deployed, then run T1–T10 on the device.
9. Delete legacy IN rows (backup first) – last, after step 4 is deployed.

Steps 1–5 (backend) and 6–7 (frontend) are independent after the API contract (`commandValue`
already in the schema) and can be done in parallel.

## 12. Effort estimate

| Block | Files | Effort |
|---|---|---|
| Backend (`cmd.go`, `address.go`, `dispatch.go`, `cache.go`, `frame.go`, `connection.go`, `service.go`, `server_settings.go`, `orm/*`, `api/dto.go`, `model/iec104.go`) | 11 | ~2 days incl. tests |
| Frontend (`models`, IN+OUT tables, `apiService`, `InPage`, `ServerPage`, tests) | 7 | ~1 day |
| Contract/docs regeneration + review | 5 | ~0.5 day |
| Total | 23 | ~3.5 days |

No data migration (legacy rows without CA bytes are edited/deleted), no new dependency. **Breaking API change:** `commonAddress` disappears from the server-settings DTOs.
