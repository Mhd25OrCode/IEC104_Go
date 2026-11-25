# IEC 60870-5-104 Client, Go

A minimal IEC 60870-5-104 ("IEC 104") client written in Go on
[`wendy512/go-iecp5`](https://github.com/wendy512/go-iecp5) v1.2.5 (`cs104`
transport + ASDU codec).

**MSL5 is the IEC 104 server** and listens on a port; **this program is the TCP
client that dials MSL5**, sends STARTDT, **sends control commands `C_*`** (COT 6,
seven types, see below) and **receives the monitoring frames `M_*`** MSL5 sends
cyclically (COT 3), keeping them in a monitoring image keyed by CA + IOA. The
roles are deliberately inverted on the application level (see
[`VK_IEC104_MIGRATION.md`](VK_IEC104_MIGRATION.md)). The client never sends
`M_*`, never answers confirmations (COT 7/10) and never answers general
interrogation, counter interrogation, read, clock sync, test or reset requests;
such frames are ignored (logged with `-verbose`).

Run exactly **one** instance per MSL5: MSL5 keeps one session ("newest wins"), so
two instances kick each other out through auto-reconnect.

## Layout

```
iec104_client/        IEC 104 client
  main.go             flags, newClient() wiring (STARTDT, connect/lost handlers), shutdown
  client_handler.go   cs104.ClientHandlerInterface adapter (ASDUs -> station, rest ignored)
  handler.go          station: monitoring image (M_*), recorded confirmations (COT 7/10)
  confirm.go          coa() and newCommandASDU() fresh-ASDU helpers
  sender.go           command sender: C_* with COT 6, periodic + send-on-change
  handler_test.go     unit tests with a capture connection
  loopback_test.go    client against a real cs104 server on 127.0.0.1
build.sh              cross-build a static linux/arm64 binary into dist/ (CGO off)
pull.sh               token-authenticated fast-forward pull from GitHub (Raspberry Pi)
deploy/               systemd unit iec104-client.service + install.sh / uninstall.sh
docs/                 architecture.md, iec104_library_bug_report.md
```

See [`docs/architecture.md`](docs/architecture.md) for the design.

## Build & run

```powershell
cd iec104_client
go build ./...        # writes .\iec104-client.exe
go test ./...         # unit + loopback tests (loopback binds 127.0.0.1 on a free port)

# dial MSL5 on the same machine, run until Ctrl+C (or pass -duration 60s)
.\iec104-client.exe -msl5-host 127.0.0.1 -msl5-port 2404 -ca 1

# same, with the library's frame/decode log
.\iec104-client.exe -msl5-host 192.168.1.10 -ca 1 -verbose
```

| Flag | Default | Meaning |
| :-- | :-- | :-- |
| `-msl5-host` | `127.0.0.1` | MSL5 address to dial |
| `-msl5-port` | `2404` | MSL5 IEC 104 port |
| `-ca` | `1` | Common Address of all outgoing commands (incoming `M_*` are keyed by their own CA) |
| `-cmd-interval` | `5s` | period of the command cycle; `0` = only send-on-change (nothing is sent on connect) |
| `-cmd-on-change` | `true` | send the commands immediately (debounced 100 ms) when a command value changes |
| `-reconnect` | `2s` | delay between reconnect attempts |
| `-duration` | `0` | run this long, then exit (`0` = until Ctrl+C / SIGTERM) |
| `-verbose` | `false` | enable the go-iecp5 library log (frames + decode errors) |

The library reconnects forever, so MSL5 may start later or restart. Wire
parameters are the library default `asdu.ParamsWide` (COT 2 bytes, CA 2 bytes,
IOA 3 bytes) and the default cs104 timers; MSL5's `TESTFR` keep-alive is answered
by the library.

## Commands sent (VK → MSL5)

The sender holds one command point per type MSL5 accepts. Every frame is built
fresh, one information object per ASDU (`SQ=0`), always with **COT 6**, CA =
`-ca`:

| IOA | Type | Value type |
| :-- | :-- | :-- |
| 100 | `C_SC_NA_1` | bool |
| 200 | `C_DC_NA_1` | 0..3 (clamped) |
| 250 | `C_RC_NA_1` | 0..3 (clamped) |
| 301 | `C_SE_NA_1` | raw int16, **not** divided by 32768 |
| 303 | `C_SE_NB_1` | int16 |
| 302 | `C_SE_NC_1` | float32 |
| 400 | `C_BO_NA_1` | uint32 |

Commands are sent only while the session is active: every `-cmd-interval`, and
≤ 100 ms after a value changed (`-cmd-on-change`). Send errors
(`ErrNotActive`, `ErrUseClosedConnection`) are dropped silently,
`ErrBufferFulled` is logged at most every 10 s.

## Monitoring received (MSL5 → VK)

`M_SP_NA_1`, `M_DP_NA_1`, `M_ME_NC_1`, `M_ME_TF_1`, `M_ME_NB_1` and `M_IT_NA_1`
(including `SQ=1` / several objects) are stored in the monitoring image keyed by
the frame's CA + IOA with value, quality and time tag (`station.Snapshot()`).
Other types are only logged with `-verbose`.

COT 7 / COT 10 command frames are recorded as confirmations and neither executed
nor answered, so MSL5's `ActCon` mirror can never re-arm the sender. Received
data never triggers an outgoing frame.

At default verbosity only lifecycle lines are printed (connected, STARTDT
confirmed, connection lost, unexpected send failures); `-verbose` adds every
received `M_*`, sent `C_*`, confirmation and ignored type.

## Tests

`go test ./...` in `iec104_client/`:

| Test | Checks |
| :-- | :-- |
| `TestIncomingMonitoringUpdatesImage`, `...UsesFrameCA`, `...MultipleInfoObjects` | the six `M_*` types land in the image keyed by frame CA + IOA, quality/time kept, `SQ=1` |
| `TestConfirmationIsNotExecuted`, `TestRejectedTypeGetsNoReply` | COT 7/10/6/8 command frames and unsupported types are never executed or answered |
| `TestVerboseGating` | no output at default verbosity |
| `TestOutOfScopeRequestsAreNotAnswered` | GI, CI, read and clock sync are not answered |
| `TestOutgoingCommandsUseActivation`, `TestDoubleCommandValueIsClamped` | all seven types: COT 6, CA, IOA, value, one object per ASDU |
| `TestSendOnChange`, `TestSenderIdempotent`, `TestSendErrorsAreNotFatal` | send-on-change, Start/Stop concurrency, send errors |
| `TestLoopbackAgainstRealServer`, `TestNoCommandLoop` | real client against an MSL5-like `cs104` server: STARTDT, commands + ActCon mirror, monitoring image, no loop, resume after server restart |

## Key implementation points

* **Do not use `wendy512/iec104/client`**: its wrapper gives callbacks no
  connection. This program uses `cs104.Client` directly, which implements
  `asdu.Connect`.
* **STARTDT comes from this side** (`SetOnConnectHandler` → `SendStartDt`).
  Sending is only possible after the confirmation, so the sender starts in
  `SetServerActiveHandler` and stops in `SetConnectionLostHandler`.
* **Never `SendReplyMirror`**: the `Get*Cmd()` getters consume the
  information-object buffer, so a mirror would be truncated. All frames are
  built fresh. The COT 44 mirror MSL5 used to return for the old `M_*` reports
  came from MSL5's server library, not from this client.

## Library limitation

`asdu.infoObjSize` has no entry for Type IDs 58–64 (`C_SC_TA_1` … `C_BO_TA_1`),
so frames of those types are dropped by the library before any handler runs.
MSL5 never sends them. Details: [`docs/iec104_library_bug_report.md`](docs/iec104_library_bug_report.md).

## End-to-end with MSL5

1. Enable the MSL5 server: `PUT <api>/api/iec104/server` with
   `{"enabled":true,"ipAddress":"0.0.0.0","port":2404}` (state `listening`).
2. Create an IN row (MSL5 → `M_*`, e.g. `M_ME_NC_1`, CA 1, IOA 301) and an OUT
   row (VK → `C_*`, e.g. `C_SC_NA_1`, CA 1, IOA 100) with linked channels.
3. Start `iec104-client -msl5-host <msl5-ip> -msl5-port 2404 -ca 1 -cmd-interval 5s -verbose`.

Expected: MSL5 reports `connected`, then `active`; the client log shows the IN
row's `M_*` values; the OUT row's `responsePayload` shows the `C_*` frame with
`cot.cause: 7` and the linked channel changes; no COT 44 / `unhandled ASDU`
lines appear. Stopping MSL5 makes the client log reconnect attempts and resume
sending after the next STARTDT.

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

The client runs on the same board as MSL5. It does not listen on any port: it
dials MSL5, which owns `2404` (default `-msl5-host 127.0.0.1 -msl5-port 2404`).

### Prerequisites

Go **1.27.1** (the version pinned in `iec104_client/go.mod`) must be available.
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
dist/iec104-client
```

`file dist/iec104-client` should report `ELF 64-bit LSB executable, ARM aarch64`.
The same script works as a cross-build from Windows/WSL when a Go 1.27.1
toolchain is present.

> If the scripts lost their executable bit (e.g. copied from Windows or a ZIP),
> run `chmod +x build.sh deploy/*.sh` first.

### Install as a systemd service

```bash
sudo ./deploy/install.sh
```

The installer creates an `iec104` system user, copies the binary to
`/opt/iec104/`, installs `deploy/iec104-client.service` into
`/etc/systemd/system/`, then runs `daemon-reload` and `enable --now`. It also
removes the legacy outstation-server unit and binary (`iec104-server.service`,
`/opt/iec104/iec104-server`) left behind by older deployments of this
repository, so nothing competes for the MSL5 session.

```bash
systemctl status iec104-client
journalctl -u iec104-client -f
```

The unit runs with `Restart=always` as the non-root `iec104` user (it only makes
outgoing connections). The library reconnects by itself; the unit is the safety net.

### Smoke test (binary directly, no systemd)

```bash
./dist/iec104-client -msl5-host 127.0.0.1 -msl5-port 2404 -ca 1 -duration 20s &
# MSL5 (or any IEC 104 server) must be listening on that port
```

### Uninstall

```bash
sudo ./deploy/uninstall.sh
```

Stops and disables the unit (plus the legacy server unit), removes the unit
files and deletes `/opt/iec104/`.

## Documentation

* [`docs/architecture.md`](docs/architecture.md) — file responsibilities,
  dispatch and confirmation flow, concurrency, MSL5 contract, testing approach.
* [`docs/iec104_library_bug_report.md`](docs/iec104_library_bug_report.md) —
  the two go-iecp5 defects this client works around.
