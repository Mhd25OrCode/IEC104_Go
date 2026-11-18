# IEC 60870-5-104 Outstation — Architecture

Design notes for the IEC 104 outstation in [`iec104_server/`](../iec104_server)
(see also the root [`README.md`](../README.md)).

- **Library stack:** [`wendy512/iec104`](https://github.com/wendy512/iec104) v1.0.4
  (IEC 104 transport / cs104 sessions) and
  [`wendy512/go-iecp5`](https://github.com/wendy512/go-iecp5) v1.2.5 (ASDU codec),
  Go 1.27, one Go module (`github.com/alfas/iec104-server`).
- **Transport:** one TCP connection per session, default port **2404**, default
  demo common address (CA) **1**.
- **Role:** outstation only. The master is the MSL5 backend's IEC 104 client and
  lives in the backend repository; this repository never ships a client binary.

---

## 1. Scope

```text
┌──────────────────────────────┐        TCP 2404 (IEC 104)        ┌───────────────────────────────┐
│ MSL5 backend (external)      │ ◄──────────────────────────────► │ iec104-server (this repo)     │
│  master / controlling station│   C_SC, C_DC, C_SE_*, C_RD_NA_1  │  outstation / controlled      │
│                              │ ───────────────────────────────► │  station                      │
│  stores answers per (CA, IOA)│   confirmations (COT 7) and      │                               │
│                              │   monitoring data (COT 5)        │  main.go     flags, listener  │
│                              │ ◄─────────────────────────────── │  handler.go  process image,   │
└──────────────────────────────┘                                  │              handlers, demo  │
                                                                  │              reporting loops  │
                                                                  │  confirm.go  fresh-ASDU       │
                                                                  │              answer builders  │
                                                                  │  handler_test.go  unit tests  │
                                                                  └───────────────────────────────┘
```

The outstation is written so that **any** standard master works: general and
counter interrogation, clock sync, test, reset process, delay acquisition and
background reporting are all implemented. The MSL5 master uses only a subset
(commands on IOA 100/200/301 plus reads), which keeps the write-then-read
verification loop free of unsolicited traffic (see §5).

## 2. Files

| File | Responsibility |
| :-- | :-- |
| `main.go` | flag parsing (`-host`, `-port`, `-ca`, `-period`, `-spontaneous`, `-spontaneous-toggle`, `-duration`, `-verbose`), building the `station`, `server.New` / `Start` / `Stop`, connect/disconnect logging, duration or SIGINT/SIGTERM shutdown |
| `handler.go` | `station` type = the process image + all `server.CommandHandler` callbacks (`OnInterrogation`, `OnCounterInterrogation`, `OnRead`, `OnClockSync`, `OnResetProcess`, `OnDelayAcquisition`, `OnTestCommand`, `OnASDU`), the demo reporting loops and the read dispatch |
| `confirm.go` | hand-built answer ASDUs (`confirmInterrogation`, `confirmSingleCmd`, `sendMeasuredFloatTime`, `sendCounterRequest`, `confirmReadError`, …) plus the `coa()` / `newCommandASDU()` helpers |
| `handler_test.go` | unit tests with a capture `asdu.Connect` implementation (see §8) |

`server.CommandHandler` is satisfied by `*station`; the library calls its methods
from the session's handler goroutine.

## 3. Session lifecycle

1. `server.New(settings, st)` registers `st` as the ASDU handler; `Start()` opens
   the listener. Nothing is reported before a master connects.
2. TCP connect → cs104 performs the STARTDT handshake; `main.go` logs
   `[server] client connected: <addr>` via `SetOnConnectionHandler`.
3. STARTDT activation → the master may send ASDUs. cs104's dispatcher parses the
   frame, **calls the matching `Get*Cmd()` getter itself**, then invokes the
   handler:

| Request | Dispatcher getter | Handler |
| :-- | :-- | :-- |
| `C_IC_NA_1` | `GetInterrogationCmd` | `OnInterrogation` |
| `C_CI_NA_1` | `GetCounterInterrogationCmd` | `OnCounterInterrogation` |
| `C_RD_NA_1` | `GetReadCmd` | `OnRead` |
| `C_CS_NA_1` | `GetClockSynchronizationCmd` | `OnClockSync` |
| `C_RP_NA_1` | `GetResetProcessCmd` | `OnResetProcess` |
| `C_CD_NA_1` | `GetDelayAcquireCommand` | `OnDelayAcquisition` |
| `C_TS_NA_1` | `GetTestCommand` | `OnTestCommand` |
| `C_SC_*`, `C_DC_*`, `C_SE_*`, `C_BO_*`, `C_TS_TA_1` | per-type getter | `OnASDU` |

4. TCP close → `SetConnectionLostHandler` logs `[server] client disconnected`.
   The demo reporting goroutines of that session stop on their next failed send
   (each loop gives up after 30 consecutive failures).
5. `s.Stop()` (on `-duration` expiry or SIGINT/SIGTERM) closes the listener.

## 4. Why every answer is a brand-new ASDU

The `Get*Cmd()` getters are destructive: they advance the ASDU's internal
information-object buffer. Because the dispatcher already called one of them,
`SendReplyMirror()` in a handler would encode an empty/truncated body and the
master would report `asdu UnmarshalBinary failed, EOF`. cs104 additionally
answers any non-nil handler error with `SendReplyMirror(UnknownTypeID)`.

The rule enforced by `confirm.go` and `handler.go`:

1. parse the request with `pack.Get*Cmd()` **only** to read parameters;
2. store the effect in the process image;
3. build a fresh `asdu.ASDU` (`asdu.NewASDU(conn.Params(), Identifier{…})` +
   `AppendInfoObjAddr` + `Append*`), set the cause and the request's common
   address, and `conn.Send` it;
4. never return a send error to the dispatcher — `replyOrLog()` logs it and
   returns `nil`.

`newCommandASDU()` is the shared constructor:
`VariableStruct{IsSequence: false, Number: 1}`, `coa(cause)` and the given CA.
Hand-building is also required because several library helpers restrict the
cause of transmission (`MeasuredValueFloatCP56Time2a` accepts only
Spontaneous/Request, `BitsString32Cmd` only Activation/Deactivation,
`IntegratedTotals` only counter-interrogation causes).

## 5. Process image and concurrency

```go
type station struct {
	ca      asdu.CommonAddr  // CA of unsolicited demo frames only
	mu      sync.Mutex
	spVal   bool             // IOA 100
	dpVal   asdu.DoublePoint // IOA 200
	ana     float32          // IOA 301 / 302
	counter int32            // IOA 400
	bits    uint32           // last C_BO_* command (not reported)
	toggle  bool             // -spontaneous-toggle
	started sync.Map         // remote addr -> struct{} (one reporting pair per session)
	period, spont time.Duration
}
```

* One mutex protects the image. Handlers lock it only to copy or write values;
  ASDUs are encoded outside the lock.
* `started` makes `startReporting` idempotent per connection address. It is
  called from `OnInterrogation` (after the STARTDT handshake) and spawns one
  periodic and one spontaneous goroutine per session:

  | Loop | Interval | Frames |
  | :-- | :-- | :-- |
  | periodic | `-period` | `M_ME_NC_1` IOA 301, COT 1 |
  | spontaneous | `-spontaneous` | `M_SP_TB_1` IOA 100 + `M_ME_TF_1` IOA 302, COT 3 |

* Both loops give up after 30 consecutive send failures (disconnected master),
  so they cannot outlive a session for long.
* `-spontaneous-toggle` is the only writer besides commands: without it the
  ticks re-publish the image, so a commanded value survives until the next
  command — that is what makes read-after-write deterministic.

## 6. Common address policy

`-ca` is used **only** for the unsolicited frames the demo loops push.
Every request-driven answer uses `pack.CommonAddr`, i.e. the CA the master put
in its request:

| Handler | CA of the answer |
| :-- | :-- |
| `OnASDU` (all `C_*` commands, `C_TS_TA_1`) | `pack.CommonAddr`, echoed into every confirmation |
| `OnRead` (data and COT 47 rejection) | `pack.CommonAddr` |
| `OnInterrogation` / `OnCounterInterrogation` | `pack.CommonAddr` for ActivationCon, data, ActivationTerm |
| `OnClockSync` / `OnResetProcess` / `OnDelayAcquisition` / `OnTestCommand` | `pack.CommonAddr` |
| `periodicLoop` / `spontaneousLoop` | `st.ca` (nobody asked, so the configured CA is the only sensible choice) |

Reason: the master indexes its point values by `(CA, IOA)`. An answer filed under
a CA the master never used becomes invisible — that was the original defect this
outstation had to fix.

## 7. Read dispatch

`OnRead` switches on the IOA and answers with the ASDU type the object actually
has (`M_SP_NA_1` 100, `M_DP_NA_1` 200, `M_ME_NA_1` 300, `M_ME_NC_1` 301,
`M_ME_TF_1` 302, `M_IT_NA_1` 400), all with COT 5 (`Request`). Any other IOA is
answered with `C_RD_NA_1` carrying COT 47 (`UnknownIOA`), so the master can show
a rejection instead of a fabricated value. `M_ME_TF_1` and `M_IT_NA_1` answers
are hand-built for the same reason as the confirmations (§4).

## 8. Testing approach

`handler_test.go` avoids sockets and ports entirely:

* `captureConn` implements `asdu.Connect` (`Params()`, `Send()`,
  `UnderlyingConn()`); `Send` marshals the ASDU to bytes and appends it to a
  slice, so every test can decode exactly what went on the wire.
* `request()` does the opposite direction: it builds a master frame with the
  library's own `asdu.SingleCmd`, `asdu.ReadCmd`, `asdu.InterrogationCmd`, …
  helpers, captures the bytes and decodes them again, producing the same
  "already-consumed by the dispatcher" ASDU the server handler receives in
  production.
* `newTestStation()` uses `time.Hour` reporting intervals, so the demo loops
  never fire during a test; tick behaviour is unit-tested directly through
  `periodicTick()` / `spontaneousTick()`.

Covered: all six command forms the master can send, the six native read
answers, the COT 47 rejection, read-after-write, the GI and CI sequences
(types, causes, CA) and the `-spontaneous-toggle` semantics.

A live end-to-end check (real TCP listener + `github.com/wendy512/iec104/client`
master, STARTDT handshake, command → read-back) was run once for the
2026-11-18 verification documented in the root README; it is intentionally not
part of the committed suite because it binds a port.

## 9. Packaging and deployment

| Path | Purpose |
| :-- | :-- |
| `build.sh` | `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w"` → `dist/iec104-server` |
| `deploy/iec104-server.service` | systemd unit: `User=iec104`, `ExecStart=/opt/iec104/iec104-server -host 0.0.0.0 -port 2404`, `Restart=always`, enabled at boot |
| `deploy/install.sh` | creates the `iec104` user, installs binary + unit, removes legacy master units/binary, `daemon-reload`, `enable --now` |
| `deploy/uninstall.sh` | stops/disables the unit (and legacy client units), removes unit files and `/opt/iec104` |
| `pull.sh` | token-authenticated `git fetch` + `--ff-only` merge on the Pi, token read from `$HOME/.iec104-git-token` |

`dist/` and `logs/` are gitignored, so a pull never overwrites build output.

## 10. Library defects that shaped the code

| # | Defect | Mitigation here |
| :-- | :-- | :-- |
| 1 | `Get*Cmd()` consumes the ASDU; `SendReplyMirror()` then encodes an empty body | fresh ASDU for every answer; `replyOrLog()` keeps handler errors away from the dispatcher's mirror |
| 2 | bundled example never implemented the GI sequence | `OnInterrogation` sends ActivationCon → data (COT 20) → ActivationTerm |
| 3 | `asdu.infoObjSize` lacks Type IDs 58–64 (`C_SC_TA_1` … `C_BO_TA_1`) → receiver drops those frames | untagged forms are used/supported; tagged branches stay in place for a patched/vendored library |

Details, A/B evidence and upstream commit references:
[`iec_60870_5_104_bug_report.md`](../iec_60870_5_104_bug_report.md). That report
predates the removal of the master client from this repository, so its
"client side" observations describe the external master.

## 11. Open items

* Type IDs 58–64 stay unusable until `go-iecp5` registers them (or a vendored
  copy is patched); the master therefore must use untagged command forms.
* `C_RC_NA_1` / `C_RC_TA_1` (step commands) are logged but not confirmed.
* Values are a static demo image (IOA 300 constant, IOA 400 constant); wiring
  them to real I/O is out of scope for this test outstation.
