# Plan — write `IEC104_SERVER_ANALYSIS_AND_RECOMMENDATIONS.md`

## Context
The data logger must run an IEC 104 **client** (southbound, already working in MSL5) and an
IEC 104 **server** (northbound, this repo) at the same time (`docs/data_logger_system_description.md`).
The server in `iec104_server/` was built as a test outstation for MSL5. It has known library bugs
and does not support all the ASDUs the gateway needs. The deliverable is **one new Markdown report at the repo root**.
No source code is changed. Everything below was verified against the repo and the library sources in the
module cache (`go-iecp5@v1.2.5`, `iec104@v1.0.4`).

## Deliverable
Create `c:\Users\alfas\IEC104_Go\IEC104_SERVER_ANALYSIS_AND_RECOMMENDATIONS.md` (only file written). Sections:

1. **Executive summary.** Top blockers, the recommended approach and the roadmap in one page.
2. **Scope and sources.** Files read, library versions and commit (`2269ee7`), what "verified" means.
3. **Current server architecture.** A short description of `main.go`, `handler.go`, `confirm.go`, and the cs104 dispatch path (`server_session.go:328-518`).
4. **Root-cause analysis of reported bugs.** Every finding cites file:line.
   - **Bug 1 (Get*Cmd + SendReplyMirror):** `Decode*` reslices `infoObj` (`asdu/codec.go`) and `SendReplyMirror` copies what is left (`asdu.go:172-177`). The app works around it, **but the library still does this itself**:
     - **NEW: `C_TS_NA_1` is answered with an empty body.** `server_session.go:481-485` calls `GetTestCommand()` and then `SendReplyMirror(ActivationCon)`. `cs104.ServerHandlerInterface` has no test handler, so `station.OnTestCommand` (`handler.go:183`) never runs. The README and architecture.md tables are wrong here, and the earlier "Test Command working" result was obtained with `C_TS_TA_1` only.
     - **NEW: all library `UnknownIOA` negative replies are truncated** for IC/CI/CS/TS/RP/CD, because the mirror is sent after the getter.
     - **NEW: mirrored negative replies never set the P/N bit.**
     - `OnASDU` error → `UnknownTypeID` mirror (`:514-515`). The app avoids this with `replyOrLog`, but as a result it **never sends negative confirmations**.
   - **Bug 2 (GI):** fixed in the app. Remaining defects:
     - QOI groups 21–36 get a full-image answer with COT 20.
     - `M_ME_TF_1` is sent with COT 20. This bypasses the library's restriction, which matches the standard (time-tagged types are not used for interrogation).
     - `time.Now()` is used instead of the source timestamp.
     - One ASDU is sent per point, and `sendASDU` has a capacity of K<<4 = 192 (`server.go`), so a large GI hits `ErrBufferFulled`.
   - **Bug 3 (infoObjSize 58–64):** this is a **critical blocker for the gateway**. Type 63 `C_SE_TC_1` (P Soll) is blocked in both directions. The server cannot receive it from VK, and the southbound client cannot decode the plant's confirmation. No application-layer workaround exists: the map is unexported and the frame is dropped before any handler runs.
   - **Application-level defects in `handler.go`:**
     - (A1) Select-before-operate is ignored. `Qoc.InSelect`/`Qos.InSelect` are not checked, so a SELECT executes immediately.
     - (A2) A command with COT Deactivation(8) is applied and confirmed with COT 7.
     - (A3) Command IOAs are not validated or whitelisted.
     - (A4) No ActivationTerm and no negative confirmations.
     - (A5) `C_RC_*` and unknown types are ignored silently instead of answered with COT 44/45.
     - (A6) The command is confirmed after a local apply. The gateway must instead decouple confirmation from the southbound result. Handlers also run on the single per-session `handlerLoop`, so a blocking forward stalls the session.
     - (A7) The `C_TS_TA_1` confirmation does not echo the received TSC/time.
     - (A8) There is no spontaneous/cyclic path. `Send` checks only the TCP state, not STARTDT.
     - (A9) `server.Start` runs `ListenAndServer` in a goroutine, and a bind error is only `clog`-logged, which is disabled by default. A port conflict therefore fails silently.
     - (A10) No `M_EI_NA_1`.
     - (A11) The process image is hard-coded: no mapping table, quality or timestamps.
     - (A12) The tests call handlers directly and bypass the cs104 dispatcher, so the library-path bugs are invisible to them.
     - A broadcast CA of 0xFFFF is echoed back instead of the station CA.
5. **ASDU support matrix.** For each TypeID: library codec, decodable (infoObjSize), server-handled, needed by the data logger.
6. **ASDU implementation priorities.**
   - **P0 (signal list / spec §5, §9):**
     - `M_ME_TF_1` 36: spontaneous, with source time and quality.
     - `M_SP_TB_1` 30.
     - `C_SE_TC_1` 63: receive, confirm, forward.
     - `C_SC_NA_1` 45 with SBO.
     - GI using the untagged equivalents `M_ME_NC_1` 13 and `M_SP_NA_1` 1 with COT 20.
     - `C_TS_NA_1` fix.
     - `C_CS_NA_1`.
     - `M_EI_NA_1`.
     - Negative confirmations.
   - **P1:**
     - The remaining 58–64 types.
     - `C_DC_*` and `M_DP_TB_1`.
     - GI groups.
     - ActivationTerm policy.
     - `C_RD_NA_1` answered from the point DB.
   - **P2:**
     - `C_RC_*`.
     - `M_ME_TD/TE`.
     - `M_IT_TB_1` and CI groups.
     - `M_ST_*` and `M_BO_*`.
   - **Not recommended:** F_*, P_*, S_*.
7. **Options evaluation: fix upstream vs fork vs extension layer.** Compare maintainability, compatibility, licensing, and dual-role use.
   - Licensing: `go-iecp5` is LGPL-3.0 and `iec104` is Apache-2.0.
   - An extension layer alone cannot fix Bug 3 or the `C_TS_NA_1` short-circuit.
   - A full fork is costly and risks drift from the MSL5 client.
   - Upstream alone has uncertain timing: v1.2.6 is the same commit as v1.2.5.
8. **Recommended solution: minimal patch fork + application extension layer.**
   - **Fork:** a pinned `go-iecp5` fork with 3 small patches:
     1. Add the infoObjSize rows 58–64.
     2. Make the C_TS_NA_1 confirmation a fresh/cloned ASDU.
     3. Clone the ASDU before the getter for negative replies and set P/N.
   - Consume the fork through a `replace` in the **main module of the combined data-logger binary**, so the client and the server share one codec. Submit the patches upstream and drop the fork once they are merged.
   - Do not fork `wendy512/iec104`.
   - **Extension layer** (separate packages, no library change), containing:
     - the point DB / mapping table;
     - validation: SBO, COT, CA/IOA whitelist, limits;
     - a negative-confirmation builder;
     - a command lifecycle tracker (received → validated → forwarded → confirmed → terminated/timeout/rejected);
     - async forwarding off the handler goroutine;
     - spontaneous reporting that is aware of STARTDT and of backpressure;
     - multi-object GI packing (≤249 bytes per ASDU);
     - quality propagation when the southbound link is lost (IV/NT);
     - a custom `clog.LogProvider` so that errors are always visible;
     - a bind check.
   - **Running the client and server together:** two independent cs104 instances in one process. The only global state is the read-only infoObjSize map, so this is safe. Ports are 2404 (plant) and 2405 (northbound) for local tests.
9. **Testing strategy.**
   - Codec round-trip table test for every TypeID; this guards the 58–64 patch.
   - Dispatcher-level tests through a real cs104 session on loopback; these would catch `C_TS_NA_1`.
   - Negative/conformance tests: unknown CA/COT/IOA/type, SBO, deactivation, GI groups, k/w, t1/t3, STOPDT.
   - `go test -race` and fuzzing of `UnmarshalBinary`/dispatch.
   - Interop tests with a third-party master and Wireshark.
   - A one-Pi end-to-end gateway rig (spec §8) running scenarios 7.A–D, link loss and stale data.
   - A PIXIT/interoperability checklist.
10. **Prioritized roadmap.**
    - **Phase 0:** reproduce the new bugs in tests and correct the README/architecture claims.
    - **Phase 1:** patch fork + `replace` + upstream PRs.
    - **Phase 2:** server conformance fixes A1–A10.
    - **Phase 3:** point DB, spontaneous reporting, GI packing.
    - **Phase 4:** integrate the MSL5 client in one binary, plus mapping and the command tracker.
    - **Phase 5:** hardening, interop and deployment.
    - Each phase lists its exit criteria.
11. **Open questions:** items from spec §12, plus whether VK requires ActivationTerm/SBO and its PIXIT.

## Critical files referenced (read-only)
- `iec104_server/handler.go`, `confirm.go`, `main.go`, `handler_test.go`, `go.mod`
- `$GOMODCACHE/github.com/wendy512/go-iecp5@v1.2.5/cs104/server_session.go`, `server.go`, `asdu/asdu.go`, `asdu/codec.go`, `asdu/csys.go`, `asdu/identifier.go`, `asdu/mproc.go`
- `$GOMODCACHE/github.com/wendy512/iec104@v1.0.4/server/*.go`
- `README.md`, `docs/architecture.md`, `docs/data_logger_system_description.md`, `iec_60870_5_104_bug_report.md`

## Verification
- Every claim in the report cites a file:line that has been checked against the sources above.
- `git status` shows only the new report file. No source changes.
- (Optional, read-only) `go test ./...` in `iec104_server` still passes, to confirm the baseline the report describes.
