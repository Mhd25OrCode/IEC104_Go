# IEC 60870-5-104 Outstation (Go)

A minimal IEC 60870-5-104 ("IEC 104") **outstation** ("slave" / server) written in
Go, built on [`wendy512/iec104`](https://github.com/wendy512/iec104) v1.0.4
(IEC 104 transport) and [`wendy512/go-iecp5`](https://github.com/wendy512/go-iecp5)
v1.2.5 (ASDU codec).

This repository contains the **outstation only**. The master that drives it is the
MSL5 backend's IEC 104 client (the `src/apps/backend/internal/device/iec104/`
package of the backend repository), which owns the single TCP session and uses a
small set of object types (see ["What the master sends"](#what-the-master-sends)).
The outstation **never sends periodic or spontaneous data**: the process image
changes only through commands and is reported only when asked — `C_RD_NA_1`
reads, plus the one-shot general/counter interrogation snapshots that non-MSL5
masters may request. Clock sync, test, reset and delay acquisition are answered so
any standard IEC 104 master works as well.

## Layout

```
iec104_server/      outstation
  main.go           flags, listener lifecycle, connect/disconnect logging
  handler.go        CommandHandler: process image, command/read dispatch
  confirm.go        fresh-ASDU confirmation/data builders (library workaround)
  handler_test.go   unit tests: commands, reads, no-GI behaviour, GI/CI snapshots
build.sh            cross-build a static linux/arm64 binary into dist/ (CGO off)
pull.sh             token-authenticated fast-forward pull from GitHub (Raspberry Pi)
deploy/             systemd unit + install.sh / uninstall.sh for the Pi
docs/               architecture.md, iec104_test_outstation_change_plan.md
```

See [`docs/architecture.md`](docs/architecture.md) for the file-by-file design,
the dispatch/confirmation flow and the testing approach.

## Build & run

```powershell
cd iec104_server
go build ./...        # writes .\iec104-server.exe
go test ./...         # unit tests (no network, no ports)

# run until Ctrl+C (or pass -duration 60s)
.\iec104-server.exe -host 0.0.0.0 -port 2404

# same, with the library's frame/decode log on stderr
.\iec104-server.exe -port 2404 -verbose
```

The executable name comes from the module path
(`github.com/alfas/iec104-server`), not from the folder name. There is no
client binary in this repository any more, so a live session needs a master:
either the MSL5 backend, or any third-party IEC 104 master pointed at
`<host>:2404`. Answers always carry the **request's** common address (MSL5 uses
the CA stored per 104 row), so no CA has to be configured on the outstation.

| Flag | Default | Meaning |
| :-- | :-- | :-- |
| `-host` | `0.0.0.0` | listen address |
| `-port` | `2404` | listen port |
| `-duration` | `0` | run this long, then exit (`0` = until Ctrl+C / SIGTERM) |
| `-verbose` | `false` | enable the go-iecp5 library log (frames + decode errors) |

There is deliberately no reporting-interval or common-address flag: the
outstation has no unsolicited traffic and every answer echoes the request CA.

`-verbose` is the only way to see ASDU decode failures: the library drops a
frame it cannot decode silently (`sf.Error()` prints nothing unless
`LogMode(true)`), which is how the `C_BO_TA_1` limitation below shows up.

## Tests

`go test ./...` runs the handler tests in `iec104_server/handler_test.go` against
a capture connection: each test builds a real master frame with the library's own
`asdu.*Cmd` helpers, decodes it again, feeds it to the outstation handler and
then decodes what the outstation sent back. Covered:

| Test | Checks |
| :-- | :-- |
| `TestCommandsApplyToProcessImage` | `C_SC`/`C_DC`/`C_SE_NA`/`C_SE_NB`/`C_SE_NC`/`C_BO` land in the process image and are confirmed with their own type, COT 7, the request CA and the request IOA |
| `TestReadAnswersWithNativeTypes` | `C_RD_NA_1` on IOA 100/200/300/301/302/400 answers `M_SP_NA_1` / `M_DP_NA_1` / `M_ME_NA_1` / `M_ME_NC_1` / `M_ME_TF_1` / `M_IT_NA_1` with COT 5 |
| `TestReadUnknownIOA` | an unmapped IOA is rejected with `C_RD_NA_1` and COT 47 |
| `TestReadBackAfterCommand` | the write-then-read pattern of the master reads back exactly what the command stored (nothing overwrites the process image) |
| `TestCommandsAndReadsWorkWithoutInterrogation` | a fresh connection that never sent a GI applies commands and answers reads immediately; exact frame counts prove no unsolicited traffic |
| `TestInterrogationSequenceEchoesRequestCA` | standard-master GI: `ActivationCon(7)` → data with COT 20 → `ActivationTerm(10)`, all on the request CA |
| `TestCounterInterrogationEchoesRequestCA` | standard-master CI: `ActivationCon(7)` → `M_IT_NA_1` COT 37 → `ActivationTerm(10)` |

## Process image

One mutex-protected process image (`station` in `handler.go`) backs every
monitored object. **Commands are the only writer**; reads and interrogation
snapshots report it, and no timer or simulation ever touches it.

| IOA | Object | Init | Reported as | Written by |
| :-- | :-- | :-- | :-- | :-- |
| 100 | single point `spVal` | `true` | `M_SP_NA_1` (read, GI snapshot) | `C_SC_NA_1` / `C_SC_TA_1` |
| 200 | double point `dpVal` | determined on (2) | `M_DP_NA_1` (read, GI snapshot) | `C_DC_NA_1` / `C_DC_TA_1` |
| 300 | normalized measurement | `10000` (= 0.305 per unit) | `M_ME_NA_1` (read, GI snapshot) | — (fixed value) |
| 301 | short float `ana` | `100` | `M_ME_NC_1` (read, GI snapshot) | `C_SE_NA_1/TA_1`, `C_SE_NB_1/TB_1`, `C_SE_NC_1/TC_1` |
| 302 | short float with CP56Time2a | `100` (same `ana`) | `M_ME_TF_1` (read, GI snapshot) | same as IOA 301 (the analog is shared) |
| 400 | integrated total `counter` | reading `0`, seq `1` | `M_IT_NA_1` (read, counter interrogation) | — (fixed value) |

`C_BO_NA_1` / `C_BO_TA_1` values are stored in a separate `bits` field and are
not reported as a monitored point (no bitstring type is in the master's model);
they are confirmed and visible in the log only.

## What the master sends

The MSL5 backend uses exactly these objects: single point IOA 100, double point
IOA 200, analog IOA 301 (and its time-tagged representation 302), plus
`C_RD_NA_1` reads. Anything else below is for standard masters.

| Request | Handler | Effect | Answer |
| :-- | :-- | :-- | :-- |
| `C_SC_NA_1`, `C_SC_TA_1` | `OnASDU` | `spVal = value` | same type, COT 7, echo IOA/QOC (SCO) |
| `C_DC_NA_1`, `C_DC_TA_1` | `OnASDU` | `dpVal = value & 0x03` | same type, COT 7, echo IOA/QOC (DCO) |
| `C_SE_NA_1`, `C_SE_TA_1` | `OnASDU` | `ana = float32(int16 value)` — raw, **not** divided by 32768 | same type, COT 7 |
| `C_SE_NB_1`, `C_SE_TB_1` | `OnASDU` | `ana = float32(int16 value)` | same type, COT 7 |
| `C_SE_NC_1`, `C_SE_TC_1` | `OnASDU` | `ana = float32 value` | same type, COT 7 |
| `C_BO_NA_1`, `C_BO_TA_1` | `OnASDU` | `bits = value` | same type, COT 7 |
| `C_IC_NA_1` | `OnInterrogation` | one-shot snapshot of the process image (MSL5 never sends it); `Deactivation` answers COT 9 | `C_IC_NA_1` COT 7 → data COT 20 → `C_IC_NA_1` COT 10 |
| `C_CI_NA_1` | `OnCounterInterrogation` | one-shot snapshot of the counter image (MSL5 never sends it) | `C_CI_NA_1` COT 7 → `M_IT_NA_1` COT 37 → `C_CI_NA_1` COT 10 |
| `C_RD_NA_1` | `OnRead` | reads the image | native type with COT 5, or `C_RD_NA_1` with COT 47 |
| `C_CS_NA_1` | `OnClockSync` | logs the time | `C_CS_NA_1` COT 7 with the echoed CP56Time2a |
| `C_TS_NA_1` | `OnTestCommand` | — | `C_TS_NA_1` COT 7 (test word) |
| `C_TS_TA_1` | `OnASDU` | — | `C_TS_TA_1` COT 7 (CP56Time2a) |
| `C_RP_NA_1` | `OnResetProcess` | — | `C_RP_NA_1` COT 7 (QRP echoed) |
| `C_CD_NA_1` | `OnDelayAcquisition` | — | `C_CD_NA_1` COT 7 (delay echoed) |
| `C_RC_NA_1`, `C_RC_TA_1` | — | logged as unhandled, no answer | — |

Command confirmations are always built **after** the value has been stored, so a
`C_RD_NA_1` issued right after a command reads back what was written.

### Read answers (`C_RD_NA_1`)

| IOA | Answer |
| :-- | :-- |
| 100 | `M_SP_NA_1` (single point, COT 5) |
| 200 | `M_DP_NA_1` (double point, COT 5) |
| 300 | `M_ME_NA_1` (normalized measurement, COT 5) |
| 301 | `M_ME_NC_1` (short float, COT 5) |
| 302 | `M_ME_TF_1` (short float + CP56Time2a, COT 5) |
| 400 | `M_IT_NA_1` (integrated total, COT 5) |
| anything else | `C_RD_NA_1` with COT 47 (unknown information object address) |

### Common address semantics

Every answer — command confirmations, read answers, interrogation data and
terminations, the COT 47 rejection — carries the common address of the
**request**. The master stores its per-point answers under `(CA, IOA)` and MSL5
uses the CA stored per 104 row, so hard-coding an outstation CA would file the
answers under a CA the master never asked for. There is no outstation-wide CA
flag.

### No unsolicited traffic

The outstation never initiates a transfer: there is no periodic, spontaneous,
background or simulated reporting, and no timer writes the process image. Commands
and reads answer immediately after the connection is up, without a general
interrogation. A general (or counter) interrogation is answered with a single
snapshot of the process image if a standard master asks for one; MSL5 never does.

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
| Single / Double / Set-point / Bitstring | `C_SC_*` / `C_DC_*` / `C_SE_*` / `C_BO_*` | Activation (6) | ActivationCon (7) |

There are no periodic or spontaneous rows: nothing is sent unless a master asked
for it. Interrogation data: `M_SP_NA_1`, `M_DP_NA_1`, `M_ME_NA_1`, `M_ME_NC_1`,
`M_ME_TF_1` (COT 20); counters `M_IT_NA_1` (COT 37).

## Verified run

`gofmt -l .` (clean), `go vet ./...`, `go test ./...` and `go build ./...` were run
on Windows with Go 1.27.4 after the demo reporting was removed. All handler tests
pass:

```text
--- PASS: TestCommandsApplyToProcessImage (0.00s)               # 6 command types
--- PASS: TestReadAnswersWithNativeTypes (0.00s)                # 6 native read answers
--- PASS: TestReadUnknownIOA (0.00s)
--- PASS: TestReadBackAfterCommand (0.00s)
--- PASS: TestInterrogationSequenceEchoesRequestCA (0.00s)      # standard-master GI snapshot
--- PASS: TestCounterInterrogationEchoesRequestCA (0.00s)       # standard-master CI snapshot
--- PASS: TestCommandsAndReadsWorkWithoutInterrogation (0.00s)  # no GI, no unsolicited frames
ok  	github.com/alfas/iec104-server
```

The live check started the real listener on an ephemeral port and drove it with
the library's own master client (`github.com/wendy512/iec104/client`, STARTDT
handshake completed). Outstation log and master observations of that session:

```text
[server] single command: IOA=100 value=false COT=COT<Activation> CA=1
[server] applied: spVal=false dpVal=2 ana=100 bits=0x00000000
master: C_SC_NA_1 -> type=C_SC_NA_1 COT=7 CA=1
[server] read command: IOA=100 CA=1
master: read IOA 100 -> type=M_SP_NA_1 COT=5 value=[{100 false ...}]
[server] setpoint float command: IOA=301 value=42.5 COT=COT<Activation> CA=1
[server] applied: spVal=false dpVal=2 ana=42.5 bits=0x00000000
[server] read command: IOA=301 CA=1
master: read IOA 301 -> type=M_ME_NC_1 COT=5 value=[{301 42.5 ...}]
[server] read command: IOA=9999 is not a monitored object
master: read IOA 9999 -> type=C_RD_NA_1 COT=47
```

This is the master's write-then-read verification pattern end to end: the
command is confirmed with COT 7, the following read answers with the object's
native type, and an unknown IOA is rejected instead of returning an invented
value.

## Key implementation points

### 1. Confirmations are fresh ASDUs, never mirrors

`GetSingleCmd()`, `GetInterrogationCmd()`, `GetCounterInterrogationCmd()`,
`GetReadCmd()`, `GetResetProcessCmd()` and `GetDelayAcquireCommand()` are not
side-effect-free getters: they **consume** the ASDU's information-object buffer.
The cs104 server dispatcher calls them *before* the application handler runs, so
a `SendReplyMirror(...)` issued in a handler is assembled from already-drained
data and reaches the master empty or truncated (`asdu UnmarshalBinary failed,
EOF`). cs104 also answers a non-nil handler error with its own
`SendReplyMirror(UnknownTypeID)`.

Therefore each handler here:

1. parses the request with `pack.Get*Cmd()` only to *read* parameters,
2. stores the value in the process image,
3. builds a brand-new ASDU (`asdu.NewASDU` + `AppendInfoObjAddr` + `Append*`)
   with the right cause and the request's common address, and sends it,
4. reports send failures through `replyOrLog()` instead of returning them, so
   the library can never emit a mirrored reply.

`confirm.go` also hand-builds `M_ME_TF_1`, `M_IT_NA_1`, `C_RD_NA_1` rejections and
`C_TS_TA_1` confirmations, because the library's helpers restrict the cause of
transmission (e.g. `asdu.MeasuredValueFloatCP56Time2a` rejects COT 20/5 and
`asdu.BitsString32Cmd` only allows Activation/Deactivation).

### 2. Values are stored before they are confirmed

`applySingle` / `applyDouble` / `applyAnalog` / `applyBits` run before the
confirmation is sent, and the whole image is logged (`[server] applied: …`), so
the outstation log can be compared with what the master shows.

### 3. Reads are typed, not generic

The master keeps `IN` values per `(CA, IOA)` and expects the ASDU type that
represents the object: answering a read of the analog with `M_SP_NA_1` would
store the answer under a type the UI never displays. `OnRead` therefore switches
on the IOA and answers with the native type, and rejects unknown IOAs with
COT 47.

## Library bugs and how they are handled

Three defects in the dependency stack (`wendy512/iec104` v1.0.4 +
`wendy512/go-iecp5` v1.2.5/v1.2.6) shape the code in `iec104_server/`. The full
reproduction notes are kept in
[`iec_60870_5_104_bug_report.md`](iec_60870_5_104_bug_report.md); that document
was written when this repository also shipped a master client, so its
client-side observations describe the peer, not code in this repository.

| # | Bug | Fails on | Responsible side | Status here |
| :-- | :-- | :-- | :-- | :-- |
| 1 | `SendReplyMirror()` after `Get*Cmd()` builds the reply from an already-consumed ASDU | the peer that decodes a command and replies | outstation | ✅ avoided: every answer is a fresh ASDU (`confirm.go`), handler errors are swallowed by `replyOrLog` |
| 2 | General interrogation never produced `ActivationCon` → data → `ActivationTerm` | the peer that answers the interrogation | outstation | ✅ implemented explicitly in `OnInterrogation` |
| 3 | `asdu.infoObjSize` has no entry for `C_SC_TA_1` … `C_BO_TA_1` (Type IDs 58–64) | any peer that *receives* one of those types | upstream `go-iecp5` | ⚠ open upstream; untagged `C_BO_NA_1` works, see below |

**Bug 3 in practice.** `ASDU.UnmarshalBinary` → `fixInfoObjSize()` →
`GetInfoObjSize()` returns `ErrTypeIdentifier` for Type IDs 58–64 and the frame
is dropped inside cs104's read loop before any handler runs (the sender sees
`Send` return `nil`; only `-verbose` shows
`asdu UnmarshalBinary failed,asdu: type identification unknown`). There is no
application-level fix, because `infoObjSize` is unexported and has no
registration API. Consequences for this outstation:

* `C_SC_TA_1`, `C_DC_TA_1`, `C_SE_TA_1`, `C_SE_TB_1`, `C_SE_TC_1` never reach
  the handlers when the master uses the **tagged** command forms — the untagged
  `C_*_NA_1` / `C_SE_N*_1` forms work and are what the MSL5 backend sends.
* `C_BO_TA_1` cannot round-trip; use the untagged `C_BO_NA_1` (registered).

The tagged branches are kept in the `OnASDU` switch so a patched/vendored
`go-iecp5` (adding the seven `infoObjSize` rows, e.g. via a `replace` directive)
needs no handler changes.

## Known limitations / deviations

| Topic | Behaviour | Why |
| :-- | :-- | :-- |
| `C_SE_NA_1` scaling | the int16 value is stored as-is, not divided by 32768 | the master sends engineering values for both int16 set-point forms; rescaling would make the read-back 32768× smaller than what the operator sent |
| `C_BO_*` bitstring | confirmed and logged, not reported as a monitored point | the master's model has no bitstring point for IOA 400 |
| `C_RC_NA_1` / `C_RC_TA_1` | logged as unhandled, no confirmation | not implemented |
| IOA 300 normalized value | constant `10000` (≈0.305 pu) | spare object for standard masters; no command writes it |
| IOA 400 counter | constant reading `0`, seq `1` | counter interrogation only; no command writes it |
| no unsolicited traffic | the outstation only answers requests | keeps a read-after-write session deterministic; process image changes only through commands |

## Pull / update (`pull.sh`)

`pull.sh` pulls the latest changes from `github.com/Mhd25OrCode/IEC104_Go` into
the working copy on the Pi. The remote is HTTPS, so a GitHub **Personal Access
Token (PAT)** is required and read from a **local file that never leaves the Pi**
(nothing token-related is ever pushed to GitHub):

```bash
# one-time setup on the Pi (token is not echoed and not written to bash history)
umask 077
read -rsp 'GitHub PAT: ' TOK; printf '%s\n' "$TOK" > "$HOME/.iec104-git-token"; unset TOK
chmod 600 "$HOME/.iec104-git-token"
```

The file holds the token on a single line, with no `Bearer` prefix and no quotes
(CR/LF and surrounding whitespace are trimmed). A fine-grained token with
`Contents: Read-only` on this one repository is enough.

```bash
./pull.sh                       # token from $HOME/.iec104-git-token, branch main
./pull.sh --token-file /root/.pat
./pull.sh --branch main --dry-run
./pull.sh --stash               # keep local edits in a git stash, then pull
./pull.sh --force               # discard local edits to tracked files, then pull
./pull.sh --clone               # bootstrap an unpacked copy into a git working copy
```

Token resolution order: `--token-file <path>` → `$IEC104_GIT_TOKEN_FILE` →
`$HOME/.iec104-git-token`. The token is never written to `.git/config` (the fetch
uses an explicit `https://x-access-token:<PAT>@…` URL and then
`git merge --ff-only FETCH_HEAD`), it is redacted from all output, and the script
refuses to touch a dirty tree unless `--stash`/`--force` is given.
`.iec104-git-token` (and `*-git-token`) are listed in `.gitignore`. Since `dist/`
and `logs/` are gitignored, pulling never clobbers build artifacts or captured
logs. A non-fast-forward upstream stops the script instead of silently resetting
the tree; `--force` only discards *your own* local edits.

## Raspberry Pi CM4 / CM5 (Debian trixie, arm64) build & deploy

The deployment target is a Raspberry Pi Compute Module 4 or 5 running a
**64-bit arm64 OS** — Debian GNU/Linux 13 (trixie) arm64 for the CM5, and
Raspberry Pi OS 64-bit (Bookworm) or Debian arm64 for the CM4 — with systemd.
The CM4 is Cortex-A72 (ARMv8-A, AArch64), so the `GOARCH=arm64` binary from
`build.sh` is natively supported; a 32-bit (armhf) OS is not supported and fails
with `Exec format error` (verify with `uname -m`, which must print `aarch64`).

The outstation runs on the same board as the backend and listens on
`0.0.0.0:2404`; the backend's master client connects to `127.0.0.1:2404`.

### Prerequisites

Go **1.27.1** (the version pinned in `iec104_server/go.mod`) must be available.
Debian 13 ships Go 1.24, which is too old, so either:

```bash
# option A - official arm64 toolchain (recommended)
#   download from https://go.dev/dl/ and unpack to /usr/local/go
export PATH=/usr/local/go/bin:$PATH
go version    # go1.27.1 linux/arm64
```

```bash
# option B - keep Debian's Go and let it fetch the pinned toolchain on first
# build (GOTOOLCHAIN=auto is the default since Go 1.21; needs internet)
```

### Build

```bash
./build.sh
```

Builds a static arm64 ELF binary (CGO disabled, no runtime deps):

```text
dist/iec104-server
```

`file dist/iec104-server` should report `ELF 64-bit LSB executable, ARM aarch64`.
The same script works as a cross-build from Windows/WSL when a Go 1.27.1
toolchain is present.

> If the scripts lost their executable bit (e.g. copied from Windows or a ZIP),
> run `chmod +x build.sh deploy/*.sh` first.

### Install as a systemd service

```bash
sudo ./deploy/install.sh
```

The installer creates an `iec104` system user, copies the binary to
`/opt/iec104/`, installs `deploy/iec104-server.service` into
`/etc/systemd/system/`, then runs `daemon-reload` and `enable --now`. It also
removes the legacy master units and binary (`iec104-client.service`,
`iec104-client-demo.service`, `/opt/iec104/iec104-client`) left behind by older
deployments of this repository, so nothing competes for the outstation link.

```bash
systemctl status iec104-server
journalctl -u iec104-server -f
```

The unit runs with `Restart=always`, as the non-root `iec104` user (port 2404 is
unprivileged).

### Smoke test (binary directly, no systemd)

```bash
./dist/iec104-server -port 2404 -duration 20s &
# then let a master connect (backend, or any IEC 104 master), or run the unit
```

### Uninstall

```bash
sudo ./deploy/uninstall.sh
```

Stops and disables the unit (plus the legacy client units), removes the unit
files and deletes `/opt/iec104/`.

## Troubleshooting

**`missing go.sum entry for module providing package github.com/spf13/cast
(imported by github.com/wendy512/iec104/client)` next to `iec104_server/go.mod`
(gopls / VS Code Go).** No file in this repository imports `…/iec104/client`
any more, and `go.mod`/`go.sum` list only `go-iecp5` and `iec104`; `go build
./...`, `go vet ./...` and `go test ./...` are green. The diagnostic comes from a
stale language-server snapshot of a temporary smoke test
(`iec104_server/zz_smoke_test.go`) that imported the client and was deleted →
fix by running **Go: Restart Language Server** from the command palette (or
close VS Code and delete `%LOCALAPPDATA%\gopls\<workspace-hash>`). Do **not** run
the suggested `go get github.com/wendy512/iec104/client@v1.0.4`: it would add
`spf13/cast` and reinstate the master-client dependency this repository
deliberately dropped.

## Documentation

* [`docs/architecture.md`](docs/architecture.md) — file responsibilities,
  dispatch and confirmation flow, concurrency, testing approach.
* [`docs/iec104_test_outstation_change_plan.md`](docs/iec104_test_outstation_change_plan.md)
  — the change plan this outstation implements (CA echo, command/read model,
  reporting behaviour, verification steps).
* [`iec_60870_5_104_bug_report.md`](iec_60870_5_104_bug_report.md) — library
  bug analysis. Historical: it was written against an earlier layout that still
  contained a master client, which has since been removed from this repository.
