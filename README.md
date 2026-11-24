# IEC 60870-5-104 Outstation, dialling client (Go)

A minimal IEC 60870-5-104 ("IEC 104") **outstation that acts as the TCP client**,
written in Go on [`wendy512/go-iecp5`](https://github.com/wendy512/go-iecp5)
v1.2.5 (`cs104` transport + ASDU codec).

The roles of the earlier versions are reversed at the TCP level: **MSL5 is the
IEC 104 server** and listens on a port; **this program dials MSL5**, sends
STARTDT, **reports its process image as spontaneous `M_*` frames** (COT 3) and
**answers the control commands MSL5 sends cyclically** (`C_SC`, `C_DC`, `C_SE`,
`C_BO`) with `ActivationCon` (COT 7) followed by `ActivationTerm` (COT 10).
On the application level it stays the *controlled station*: it owns the process
image and MSL5 commands it. There is no general interrogation, counter
interrogation, read, clock sync, test or reset handling; such requests are
ignored (logged with `-verbose`) and never answered.

Run exactly **one** instance per MSL5: MSL5 keeps one session ("newest wins"), so
two instances kick each other out through auto-reconnect.

## Layout

```
iec104_server/        dialling outstation
  main.go             flags, newClient() wiring (STARTDT, connect/lost handlers), shutdown
  client_handler.go   cs104.ClientHandlerInterface adapter (commands -> station, rest ignored)
  handler.go          station: process image, command dispatch, ActCon + ActTerm
  confirm.go          fresh-ASDU confirmation/data builders (library workaround)
  reporter.go         spontaneous reporting (connect, interval, after commands)
  handler_test.go     unit tests with a capture connection
  loopback_test.go    client against a real cs104 server on 127.0.0.1
build.sh              cross-build a static linux/arm64 binary into dist/ (CGO off)
pull.sh               token-authenticated fast-forward pull from GitHub (Raspberry Pi)
deploy/               systemd unit + install.sh / uninstall.sh for the Pi
docs/                 architecture.md
iec104-vk-implementation-handoff.md   migration plan this implementation follows
```

See [`docs/architecture.md`](docs/architecture.md) for the design.

## Build & run

```powershell
cd iec104_server
go build ./...        # writes .\iec104-server.exe
go test ./...         # unit + loopback tests (loopback binds 127.0.0.1 on a free port)

# dial MSL5 on the same machine, run until Ctrl+C (or pass -duration 60s)
.\iec104-server.exe -msl5-host 127.0.0.1 -msl5-port 2404 -ca 1

# same, with the library's frame/decode log
.\iec104-server.exe -msl5-host 192.168.1.10 -ca 1 -verbose
```

| Flag | Default | Meaning |
| :-- | :-- | :-- |
| `-msl5-host` | `127.0.0.1` | MSL5 address to dial |
| `-msl5-port` | `2404` | MSL5 IEC 104 port |
| `-ca` | `1` | Common Address of all spontaneous frames (confirmations echo the command's CA) |
| `-spont-interval` | `5s` | period of the full spontaneous report; `0` = only on connect/change |
| `-cyclic` | `false` | send `M_ME_NC_1`/`M_ME_NB_1` with COT 1 instead of COT 3 |
| `-reconnect` | `2s` | delay between reconnect attempts |
| `-duration` | `0` | run this long, then exit (`0` = until Ctrl+C / SIGTERM) |
| `-verbose` | `false` | enable the go-iecp5 library log (frames + decode errors) |

The library reconnects forever, so MSL5 may start later or restart. Wire
parameters are the library default `asdu.ParamsWide` (COT 2 bytes, CA 2 bytes,
IOA 3 bytes) and the default cs104 timers; MSL5's `TESTFR` keep-alive is answered
by the library.

## Process image and spontaneous reporting

One mutex-protected process image (`station`). **Commands are the only writer**,
except the `M_IT` counter, which is incremented on each interval tick.

| IOA | Type | Source | MSL5 writes to |
| :-- | :-- | :-- | :-- |
| 100 | `M_SP_NA_1` | `spVal` | digital out |
| 200 | `M_DP_NA_1` | `dpVal` (reported as 0/1) | digital out |
| 301 | `M_ME_NC_1` | `ana` (float32) | analog out |
| 302 | `M_ME_TF_1` | `ana`, CP56Time2a = now | analog out |
| 303 | `M_ME_NB_1` | `int16(ana)`, clamped | analog out |
| 400 | `M_IT_NA_1` | `counter` | analog out |

`M_ME_NA_1` is deliberately not reported (MSL5 cannot map it). `C_BO_*` values go
to a separate `bits` field and are not reported.

The reporter sends the full image (six frames, one object per ASDU, COT 3, CA =
`-ca`):

1. right after STARTDT is confirmed (this activates the MSL5 session),
2. every `-spont-interval` while active,
3. ≤ 100 ms after a command changed the image.

Nothing is sent while the session is not active; send errors
(`ErrNotActive`, `ErrUseClosedConnection`) are dropped silently and
`ErrBufferFulled` is logged.

## Commands answered

| Request | Effect | Answer (same type, CA, IOA, value) |
| :-- | :-- | :-- |
| `C_SC_NA_1` (`_TA_1`) | `spVal = value` | COT 7, then COT 10 |
| `C_DC_NA_1` (`_TA_1`) | `dpVal = value & 0x03` | COT 7, then COT 10 |
| `C_SE_NA_1` (`_TA_1`) | `ana = float32(int16 value)`, raw, **not** divided by 32768 | COT 7, then COT 10 |
| `C_SE_NB_1` (`_TB_1`) | `ana = float32(int16 value)` | COT 7, then COT 10 |
| `C_SE_NC_1` (`_TC_1`) | `ana = float32 value` | COT 7, then COT 10 |
| `C_BO_NA_1` (`_TA_1`) | `bits = value` | COT 7, then COT 10 |
| `C_IC`, `C_CI`, `C_RD`, `C_CS`, `C_TS`, `C_RP`, `C_CD` | none | none (ignored) |

Deactivation commands are ignored. The value is stored before the confirmations
are sent, and a report with the new value follows. MSL5 matches confirmations by
CA + IOA + exact command type, so the confirmations echo the **request's** CA.

## Tests

`go test ./...` in `iec104_server/`:

| Test | Checks |
| :-- | :-- |
| `TestCommandsApplyToProcessImage` | each command type updates the image and yields exactly ActCon (7) then ActTerm (10) on the request CA/IOA, plus one report kick |
| `TestOutOfScopeRequestsAreNotAnswered` | GI, CI, read and clock sync are not answered |
| `TestReportAllFrames` / `TestReportUsesImageValues` | six frames in table order, COT 3, CA, IOA, time tag, image values, double-point 0/1, int16 clamp |
| `TestCyclicModeOnlyForMeasurements` | `-cyclic` switches only `M_ME_NC_1`/`M_ME_NB_1` to COT 1 |
| `TestReporterDoesNotSendWhenStopped`, `TestReporterStartStopIdempotent`, `TestReporterKickReportsChange` | reporter lifecycle, concurrency, debounced kick |
| `TestSendErrorsAreNotFatal` | send errors never panic or propagate |
| `TestLoopbackAgainstRealServer` | real client against a `cs104` server: STARTDT, image, command ActCon/ActTerm, change report, reconnect to a restarted server |

## Key implementation points

* **Do not use `wendy512/iec104/client`**: its wrapper gives callbacks no
  connection, so commands could not be answered. This program uses
  `cs104.Client` directly, which implements `asdu.Connect`.
* **STARTDT comes from this side** (`SetOnConnectHandler` → `SendStartDt`).
  Sending is only possible after the confirmation, so the reporter starts in
  `SetServerActiveHandler` and stops in `SetConnectionLostHandler`.
* **Confirmations are fresh ASDUs, never `SendReplyMirror`**: the `Get*Cmd()`
  getters consume the information-object buffer, so a mirror would be truncated.
  Handler send errors are logged by `replyOrLog`, never returned. Hand-building
  is also needed because library helpers restrict the cause of transmission.
* `asdu.Single`/`Double` reject COT 1; `M_ME_TF_1` and `M_IT_NA_1` are
  hand-built.

## Library limitation

`asdu.infoObjSize` has no entry for Type IDs 58–64 (`C_SC_TA_1` … `C_BO_TA_1`),
so frames of those types are dropped by the library before any handler runs.
MSL5 never sends them. Details: [`iec_60870_5_104_bug_report.md`](iec_60870_5_104_bug_report.md).
`IEC104_SERVER_ANALYSIS_AND_RECOMMENDATIONS.md` is kept as history; its GI/CI/RD/CS
findings no longer apply to this scope.

## End-to-end with MSL5

1. Enable the MSL5 server: `PUT <api>/api/iec104/server` with
   `{"enabled":true,"ipAddress":"0.0.0.0","port":2404}` (state `listening`).
2. Create an OUT row (CA 1, IOA 301, `asduRetTyp` `M_ME_NC_1`, linked analog
   channel) and an IN row (`C_SC_NA_1`, CA 1, IOA 100, interval `5 sek`).
3. Start `iec104-server -msl5-host <msl5-ip> -msl5-port 2404 -ca 1 -verbose`.

Expected: MSL5 reports `connected`, then `active` after the first frame; the OUT
row's `responsePayload` updates every `-spont-interval`; the IN row shows
`C_SC_NA_1` with `cot.cause: 10`; the command changes `spVal` and an
`M_SP_NA_1` follows immediately. Stopping MSL5 makes the client log reconnect
attempts and resume after the next STARTDT.

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

The outstation runs on the same board as MSL5. It does not listen on any port: it
dials MSL5, which owns `2404` (default `-msl5-host 127.0.0.1 -msl5-port 2404`).

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
deployments of this repository, so nothing competes for the MSL5 session.

```bash
systemctl status iec104-server
journalctl -u iec104-server -f
```

The unit runs with `Restart=always` as the non-root `iec104` user (it only makes
outgoing connections). The library reconnects by itself; the unit is the safety net.

### Smoke test (binary directly, no systemd)

```bash
./dist/iec104-server -msl5-host 127.0.0.1 -msl5-port 2404 -ca 1 -duration 20s &
# MSL5 (or any IEC 104 server) must be listening on that port
```

### Uninstall

```bash
sudo ./deploy/uninstall.sh
```

Stops and disables the unit (plus the legacy client units), removes the unit
files and deletes `/opt/iec104/`.

## Documentation

* [`docs/architecture.md`](docs/architecture.md) — file responsibilities,
  dispatch and confirmation flow, concurrency, testing approach.
* [`iec104-vk-implementation-handoff.md`](iec104-vk-implementation-handoff.md)
  — the migration plan this client implementation follows.
* [`docs/iec104_test_outstation_change_plan.md`](docs/iec104_test_outstation_change_plan.md)
  — superseded plan of the former server role.
* [`iec_60870_5_104_bug_report.md`](iec_60870_5_104_bug_report.md) — library
  bug analysis. Historical: it was written against an earlier layout that still
  contained a master client, which has since been removed from this repository.
