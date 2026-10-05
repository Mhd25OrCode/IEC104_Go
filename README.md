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
  main.go        connection lifecycle, stdio bridge wiring (-demo skips it), -demo command sequence
  call.go        ASDUCall implementation (all replies & data), forwards every event to the bridge
  bridge.go      NDJSON stdin/stdout bridge (ready / dispatch -> ack / event), see IEC104_GO_BRIDGE.md
  bridge_test.go pipe-based round-trip tests of the bridge protocol
  typemap.go     asduTyp <-> asdu.TypeID conversion for the bridge protocol
build.sh         cross-build static linux/arm64 binaries into dist/ (CGO off)
pull.sh          token-authenticated fast-forward pull from GitHub (Raspberry Pi)
deploy/          systemd units + install.sh / uninstall.sh for the Pi
docs/            architecture.md, iec104_client_architecture.md
```

See [`docs/architecture.md`](docs/architecture.md) and
[`docs/iec104_client_architecture.md`](docs/iec104_client_architecture.md) §7
for the bridge design and [`IEC104_GO_BRIDGE.md`](IEC104_GO_BRIDGE.md) for the wire protocol.

## Build & run

```powershell
# build both - run each module's build from inside that module's folder
cd iec104_server; go build ./...      # writes .\iec104_server\iec104-server.exe
cd ..\iec104_client; go build ./...   # writes .\iec104_client\iec104-client.exe

# terminal 1 - outstation (runs until Ctrl+C, or pass -duration)
.\iec104_server\iec104-server.exe -host 0.0.0.0 -port 2404 -duration 60s

# terminal 2 - master: by default it runs as the stdio bridge, reading NDJSON
# "dispatch" requests on stdin and writing "ready"/"ack"/"event" lines to stdout
# (normally the MSL5 backend spawns it; stdin EOF shuts it down). With -demo it
# skips the bridge entirely and instead sends the built-in demo command
# sequence once the outstation confirms STARTDT
.\iec104_client\iec104-client.exe -host 127.0.0.1 -port 2404 -duration 40s          # stdio bridge mode
.\iec104_client\iec104-client.exe -host 127.0.0.1 -port 2404 -demo -duration 40s   # demo only, no bridge
```

The executable names come from the module paths (`github.com/alfas/iec104-server`
/ `-client`), not from the folder names, which is why `go build ./...` inside each
folder produces exactly the `iec104-server.exe` / `iec104-client.exe` used above.
Note that `cd iec104_server; go build ./...` followed by a plain `cd iec104_client`
fails - the second `cd` is relative to `iec104_server` - so use `cd ..\iec104_client`.

To capture a run to files, start the outstation with `Start-Process` redirection
instead of PowerShell's `2>` (in PowerShell 5.1 `2>` wraps the native stderr of the
client in a `NativeCommandError` record and writes UTF-16). This is exactly how the
2026-09-25 run below was captured:

```powershell
$srv = Start-Process .\iec104_server\iec104-server.exe -ArgumentList '-host','0.0.0.0','-port','2404','-duration','60s' `
       -RedirectStandardOutput logs\srv.out -RedirectStandardError logs\srv.err -PassThru -NoNewWindow
Start-Sleep -Seconds 2
cmd /c ".\iec104_client\iec104-client.exe -host 127.0.0.1 -port 2404 -duration 40s 1> logs\client.out 2> logs\client.err"
$srv.WaitForExit()
```

Flags: `-host`, `-port`, `-ca` (common address, default 1),
server: `-period`/`-spontaneous` (reporting intervals),
both: `-duration` (how long to run; **default `0`** = run until Ctrl+C/SIGTERM, or, in bridge mode, until stdin closes),
both: `-verbose` (enable the go-iecp5 frame/decode log, off by default).
Client only: `-demo` (skip the stdio bridge entirely and instead send the
built-in demo command sequence once the outstation is active, off by default).
In bridge mode **stdout carries only protocol lines**; every log/diagnostic
(including the `-verbose` library log) goes to stderr.

## Pull / update (`pull.sh`)

`pull.sh` is step 1 of the Raspberry Pi workflow: it pulls the latest changes
from `github.com/Mhd25OrCode/IEC104_Go` into the working copy. The remote is
HTTPS, so a GitHub **Personal Access Token (PAT)** is required, and it is read
from a **local file that never leaves the Pi** (nothing token-related is ever
pushed to GitHub):

```bash
# one-time setup on the Pi (token is not echoed and not written to bash history)
umask 077
read -rsp 'GitHub PAT: ' TOK; printf '%s\n' "$TOK" > "$HOME/.iec104-git-token"; unset TOK
chmod 600 "$HOME/.iec104-git-token"

# optional sanity check: expect 200
curl -sS -o /dev/null -w '%{http_code}\n' \
  -H "Authorization: Bearer $(< "$HOME/.iec104-git-token")" \
  https://api.github.com/repos/Mhd25OrCode/IEC104_Go
```

The file holds the token on a single line, with no `Bearer` prefix and no
quotes (CR/LF and surrounding whitespace are trimmed). A fine-grained token with
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
`$HOME/.iec104-git-token`. The token is never written to `.git/config` (the
fetch uses an explicit `https://x-access-token:<PAT>@…` URL and then
`git merge --ff-only FETCH_HEAD`), it is redacted from all output, and the
script refuses to touch a dirty tree unless `--stash`/`--force` is given.
`.iec104-git-token` (and `*-git-token`) are also listed in `.gitignore` and
`.git/info/exclude` as a second line of defence. Since `dist/` and `logs/` are
gitignored, pulling never clobbers build artifacts or captured logs.

A non-fast-forward upstream (force-push) stops the script instead of silently
resetting the tree; `--force` only discards *your own* local edits.

## Raspberry Pi CM4 / CM5 (Debian trixie, arm64) build & deploy

The deployment target is a Raspberry Pi Compute Module 4 or 5 running a
**64-bit arm64 OS** — Debian GNU/Linux 13 (trixie) arm64 for the CM5, and
**Raspberry Pi OS 64-bit (Bookworm) or Debian arm64** for the CM4 — with
systemd. The CM4 is Cortex-A72 (ARMv8-A, AArch64), so the `GOARCH=arm64`
binaries from `build.sh` are natively supported; a 32-bit (armhf) OS is not
supported and fails with `Exec format error` (verify with `uname -m`, which
must print `aarch64`).

Both programs run on the same board: the outstation on `0.0.0.0:2404` and the
master connecting to it over loopback. The master is not a systemd service: the
MSL5 backend spawns `/opt/iec104/iec104-client` as a child process
(`IEC104_BRIDGE_BIN`) and talks to it over stdin/stdout (NDJSON).

### End-to-end workflow (pull → build → install)

```bash
cd ~/IEC104_Go
./pull.sh                  # 1) pull   - see "Pull / update" above
./build.sh                 # 2) build  - dist/iec104-{server,client} (static arm64)
sudo ./deploy/install.sh   # 3) run    - /opt/iec104 + systemd enable --now
```

### Prerequisites

Go **1.27.1** (the version pinned in both `go.mod` files) must be available.
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

Builds static arm64 ELF binaries (CGO disabled, no runtime deps) into `dist/`:

```text
dist/iec104-server
dist/iec104-client
```

`file dist/iec104-server` should report `ELF 64-bit LSB executable, ARM aarch64`.
The same script works as a cross-build from Windows/WSL when a Go 1.27.1
toolchain is present.

> If the scripts lost their executable bit (e.g. copied from Windows or a ZIP),
> run `chmod +x build.sh deploy/*.sh` first.

### Install as systemd services

```bash
sudo ./deploy/install.sh
```

The installer creates an `iec104` system user, copies the binaries to
`/opt/iec104/`, and installs two units into `/etc/systemd/system/`:
`deploy/iec104-server.service` and `deploy/iec104-client-demo.service` (the
`-demo` master). It then runs `daemon-reload` and `enable --now` on the server
unit only - the demo unit is installed for manual use and is never enabled (see
below). There is deliberately **no** unit for the bridge client: the backend
spawns it. A leftover `iec104-client.service` from an older (gRPC) deployment is
removed by the installer.

```bash
systemctl status iec104-server
journalctl -u iec104-server -f
```

### Bridge mode vs. demo mode

The bridge client (`iec104-client` without flags other than host/port) is
launched by the MSL5 backend, never by systemd. Set `IEC104_BRIDGE_BIN=/opt/iec104/iec104-client`
and, if needed, `IEC104_BRIDGE_ARGS` (e.g. `["-host","127.0.0.1","-port","2404"]`);
the backend restarts it with backoff if it exits, and its stderr appears in the
backend log prefixed with `[bridge]`.

`iec104-client-demo.service` (built-in `-demo` command sequence, no bridge) is
the only client unit. Only one master may own the outstation link, so **stop the
backend (or disable its bridge) before starting it**:

```bash
sudo systemctl start iec104-client-demo
sudo systemctl stop  iec104-client-demo
journalctl -u iec104-client-demo -f
```

The demo unit deliberately has **no `[Install]` section**: it is manual-only,
so `systemctl enable` refuses it and `systemctl preset-all` can never start it
at boot. It runs with an explicit `-duration 0` — `0` is now the flag default
(run until stopped), but the unit states it so the intent survives any future
default change — so it stays up until you stop it, and uses
`Restart=on-failure` instead of `Restart=always` so neither a clean exit nor a
crash loop can replay the demo. Port 2404 is unprivileged, so the services run as
the non-root `iec104` user.

### Smoke test (binaries directly, no systemd)

```bash
./dist/iec104-server -host 0.0.0.0 -port 2404 -duration 20s &
./dist/iec104-client -host 127.0.0.1 -port 2404 -demo -duration 15s
```

### Uninstall

```bash
sudo ./deploy/uninstall.sh
```

Stops and disables the units (including a legacy `iec104-client.service`),
removes the unit files, and deletes `/opt/iec104/`.

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
| Bitstring-32 (untagged) | `C_BO_NA_1` | Activation (6) | ActivationCon (7) |
| Bitstring-32 + CP56Time2a | `C_BO_TA_1` | Activation (6) | ActivationCon (7) — frame dropped by the unpatched library, see §14 of the bug report |
| Periodic data | `M_ME_NC_1` | Periodic (1) | — |
| Spontaneous data | `M_SP_TB_1` + `M_ME_TF_1` (CP56-tagged) | Spontaneous (3) | — |

Monitoring data sent in the interrogation response: `M_SP_NA_1`, `M_DP_NA_1`,
`M_ME_NA_1`, `M_ME_NC_1`, `M_ME_TF_1` (COT 20); counters `M_IT_NA_1` (COT 37).
Background reporting: `M_ME_NC_1` IOA 301 (COT 1) every `-period`, plus
`M_SP_TB_1` IOA 100 and `M_ME_TF_1` IOA 302 (COT 3) every `-spontaneous`.
IOA 302 carries the *same* analog value as IOA 301 (the spontaneous loop reads the
process image that the periodic loop advances), and that analog drifts by +12.5 per
period and wraps to -30 once it would exceed 220 — so a long run eventually reports
negative values on both IOAs.

## Verified run (2026-09-25)

The two commands above were re-run unchanged (outstation `-duration 60s`, master
`-duration 40s`, logs kept in `logs/`), and every claim in this README and in
`iec_60870_5_104_bug_report.md` was re-checked against the result.

| Observation | Result |
| :-- | :-- |
| Demo command sequence | 11/11 sent after STARTDT; every expected confirmation arrived with `COT=ActivationCon(7)` |
| General interrogation | `ActivationCon(7)` → `M_SP_NA_1`/`M_DP_NA_1`/`M_ME_NA_1`/`M_ME_NC_1`/`M_ME_TF_1` (COT 20) → `ActivationTerm(10)` |
| Counter interrogation | `ActivationCon(7)` → `M_IT_NA_1` IOA 400 (COT 37) → `ActivationTerm(10)` |
| Read | `M_SP_NA_1` IOA 100 with `COT=Request(5)` |
| `C_BO_NA_1` (51) | decoded by the peer, `ActivationCon` received, IOA 4001 = `0x0F0F0F0F` |
| `C_BO_TA_1` (64) | dropped by the peer's decoder — no handler call, no confirmation (§14) |
| Background reporting over 40 s | 13 × `M_ME_NC_1` IOA 301 (COT 1) + 19 × `M_SP_TB_1` IOA 100 and 19 × `M_ME_TF_1` IOA 302 (COT 3) |
| Analog drift / wrap | IOA 301 and 302 walked 100.000 → 212.500 → **-30.000** → -17.5 → -5.0 → 7.5 |
| Shutdown | client `StopDt` at 40 s; outstation exits at 60 s (`server stopped`) |
| Client TCP connections per session | 2 (the first is closed immediately by the library's auto-connect) — normal, also visible in earlier runs |

Master side of that run:

```text
[client] general interrogation sent
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

With `-verbose` the same session shows the silent type-64 drop next to the working
type-51 twin:

```text
cs104 server => [D]: RX Raw[68 18 12 00 00 00 40 01 06 00 01 00 a0 0f 00 ff 00 ff 00 ...]  <- C_BO_TA_1 (64)
cs104 server => [E]: asdu UnmarshalBinary failed,asdu: type identification unknown
cs104 server => [D]: RX Raw[68 11 14 00 00 00 33 01 06 00 01 00 a1 0f 00 0f 0f 0f 0f]     <- C_BO_NA_1 (51)
cs104 server => [D]: ASDU TID<C_BO_NA_1> COT<Activation> @1
[server] bitstring32 command: IOA=4001 value=0x0F0F0F0F COT=COT<Activation>
```

Without `-verbose` the outstation log holds only the `[server]` application lines
(716 bytes for the whole 60 s run) — the dropped frame leaves no trace at all,
which is why the flag exists.

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

The same rule drives `sendMeasuredFloatTime` (hand-built `M_ME_TF_1`):
`asdu.MeasuredValueFloatCP56Time2a` accepts only COT Spontaneous/Request, and
`asdu.BitsString32Cmd` only `Activation`/`Deactivation`, so both confirmations
and the GI-time-tagged measurement are assembled by hand.

## Library bugs and how they are handled

Three defects were found in the dependency stack while building this system
(`wendy512/iec104` v1.0.4 + `wendy512/go-iecp5` v1.2.5/v1.2.6). The full
verification — reproduction steps, A/B evidence and upstream commit hashes — is in
`iec_60870_5_104_bug_report.md`; the summary is repeated here because these bugs
directly shape the code in `iec104_server/`.

| # | Bug | Fails on | Responsible side | Status in this repo |
| :-- | :-- | :-- | :-- | :-- |
| 1 | `SendReplyMirror()` after `Get*Cmd()` builds the reply from an already-consumed ASDU | the peer that *decodes the command and replies* | **server / outstation** | ✅ Resolved (workaround in `confirm.go`) |
| 2 | General interrogation never produced `ActivationCon` → data → `ActivationTerm` | the peer that *answers the interrogation* | **server / outstation** | ✅ Resolved (`station.OnInterrogation`) |
| 3 | `asdu.infoObjSize` has no entry for `C_SC_TA_1` … `C_BO_TA_1` (Type IDs 58–64) | any peer that *receives* one of those types | **upstream `go-iecp5`** (neither app side) | ⚠ Open — untagged `C_BO_NA_1` used instead |

### Bug 1 — `Get*Cmd()` + `SendReplyMirror()`

`GetSingleCmd()`, `GetInterrogationCmd()`, `GetCounterInterrogationCmd()`,
`GetReadCmd()`, `GetResetProcessCmd()` and `GetDelayAcquireCommand()` are not
side-effect-free getters: they **consume / advance the ASDU's internal
information-object buffer**. cs104's server dispatcher calls them *before* the
application handler runs, so a `pack.SendReplyMirror(...)` issued in the handler is
assembled from already-drained data and arrives empty or truncated. The peer then
reports:

```text
asdu UnmarshalBinary failed, EOF
```

It is not enough to avoid calling the getter yourself: the dispatcher may already
have called it, and cs104 *also* answers a non-nil handler error with
`SendReplyMirror(UnknownTypeID)` on its own. **The defect therefore belongs to the
replying (server / outstation) side; the client is only the victim that prints the
error.**

Fix used here (`iec104_server/confirm.go`): treat every ASDU as consumed after a
`Get*Cmd()`, use it only to *read* parameters, and build a **brand-new ASDU**
(`asdu.NewASDU` + `AppendInfoObjAddr` + `Append*`) with `Cause = ActivationCon`.
`handler.go` additionally routes handler errors through `replyOrLog()` so the
library cannot emit its own mirrored reply. See "Key implementation point" above
and bug report §2–§4 / §13.

### Bug 2 — General interrogation sequence

With the library's bundled example the client sent `C_IC_NA_1` and the server
received it, but the standard three-step sequence

```text
client: C_IC_NA_1  COT=Activation(6)     QOI=20
server: C_IC_NA_1  COT=ActivationCon(7)  QOI=20
server: M_*        COT=InterrogatedByStation(20)
server: C_IC_NA_1  COT=ActivationTerm(10) QOI=20
```

was never observed. The interrogating **master is not at fault** — it only sends the
activation; the answering **outstation** owns the confirmation and the termination,
so this is a server-side defect (an application/example defect, distinct from
Bug 1 and not fixed by Bug 1's workaround alone).

Fix used here: `station.OnInterrogation` hand-builds the whole sequence —
`confirmInterrogation(..., ActivationCon, ...)` → `sendAllData(...)` with
`COT=InterrogatedByStation(20)` → `confirmInterrogation(..., ActivationTerm, ...)`.
A `Deactivation(8)` request is answered with `DeactivationCon(9)`. Verified
end-to-end on 2026-09-25 (see the run section above). Bug report §6 / §7 / §13.

### Bug 3 — `infoObjSize` has no entry for Type IDs 58–64

`go-iecp5`'s `asdu/identifier.go` defines the time-tagged command group
`C_SC_TA_1`, `C_DC_TA_1`, `C_RC_TA_1`, `C_SE_TA_1`, `C_SE_TB_1`, `C_SE_TC_1`,
`C_BO_TA_1` (Type IDs 58–64) but registers **none** of them in the unexported
`infoObjSize` map. `ASDU.UnmarshalBinary` → `fixInfoObjSize()` → `GetInfoObjSize()`
therefore returns `ErrTypeIdentifier`, and the frame is **dropped inside cs104's
read loop before any handler runs**, with no error at the sender (`Send` returns
`nil`). The drop is also silent by default — only `-verbose` (`LogMode(true)`)
surfaces it:

```text
cs104 server => [E]: asdu UnmarshalBinary failed,asdu: type identification unknown
```

**Responsibility: the upstream library (`wendy512/go-iecp5`), not either
application side.** The defect is present in both v1.2.5 and v1.2.6 (identical
commit `2269ee7`) and is direction-agnostic: it hits whichever peer *receives*
those types. In this demo that is the **server**, which drops the client's
`C_BO_TA_1` while the registered untagged twin `C_BO_NA_1` (51) completes its
round-trip in the same session.

Because `infoObjSize` is unexported and there is no registration API, **there is no
application-level fix**: only a patched/vendored library (adding the seven rows) or
the untagged `C_BO_NA_1` works. This repository keeps the complete `C_BO_TA_1`
code path in place and sends the working `C_BO_NA_1` alongside it — see the next
section and bug report §14.

## Known limitation — `C_BO_TA_1` (bug report §14)

Bug 3 above means `C_BO_TA_1` cannot complete a `MarshalBinary → UnmarshalBinary`
round-trip against an unmodified published `go-iecp5`. The application code here is
complete; only the codec table is missing. Options:

1. **Patch / vendor the library** — add the seven `infoObjSize` rows (58–64) and
   build against a patched copy, e.g. `replace github.com/wendy512/go-iecp5 =>
   ./third_party/go-iecp5` in both modules, or `go mod vendor` + edit. This is the
   only way to make `C_BO_TA_1` round-trip.
2. **Use the untagged twin `C_BO_NA_1` (51)** — registered and verified working
   end-to-end; the demo sends it alongside `C_BO_TA_1` (IOA 4001 vs. IOA 4000).
3. **A custom type ID is not an option** — the type byte on the wire must be 64 for
   a `C_BO_TA_1`, and the receiver validates it by that byte.

Re-verified on 2026-09-25 (see the run section above): the type-64 frame is still
dropped while the type-51 twin completes its round-trip. The complete `C_BO_TA_1`
code path stays in place (`confirmBitsString32Cmd`, the `OnASDU` case, the demo
command), so patching the size table is the only change needed once upstream fixes
its decoder.