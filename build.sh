#!/usr/bin/env bash
#
# build.sh - build the IEC 60870-5-104 master + outstation for the
# Raspberry Pi CM5 (Debian GNU/Linux 13 "trixie", 64-bit arm64).
#
# The binaries are pure Go (CGO disabled), so this produces static ELF
# executables with no runtime dependencies. It can be run:
#   * natively on the CM5 (recommended), or
#   * as a cross-build from any host with a Go >= 1.27.1 toolchain
#     (the version pinned in both go.mod files).
#
# Output: dist/iec104-server, dist/iec104-client
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="$ROOT/dist"

# Raspberry Pi CM5 = 64-bit ARM (AArch64).
export CGO_ENABLED=0
export GOOS=linux
export GOARCH=arm64

# Strip symbols + build paths for smaller, more reproducible binaries.
LDFLAGS="-s -w"

mkdir -p "$OUT"

echo ">> building iec104-server (linux/arm64)"
( cd "$ROOT/iec104_server" && go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/iec104-server" . )

echo ">> building iec104-client (linux/arm64)"
( cd "$ROOT/iec104_app" && go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/iec104-client" . )

echo ">> done - artifacts:"
ls -l "$OUT"
if command -v file >/dev/null 2>&1; then
	file "$OUT/iec104-server" "$OUT/iec104-client"
fi
