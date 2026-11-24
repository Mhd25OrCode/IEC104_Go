#!/usr/bin/env bash
#
# build.sh - build the IEC 60870-5-104 client (outstation) for the Raspberry Pi
# CM4/CM5 (64-bit arm64: Debian GNU/Linux 13 "trixie" or Raspberry Pi OS
# 64-bit).
#
# The binary is pure Go (CGO disabled), so this produces a static ELF executable
# with no runtime dependencies. It can be run:
#   * natively on the Pi (recommended), or
#   * as a cross-build from any host with a Go >= 1.27.1 toolchain
#     (the version pinned in iec104_client/go.mod).
#
# Output: dist/iec104-client
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="$ROOT/dist"

# Raspberry Pi CM4/CM5 = 64-bit ARM (AArch64).
export CGO_ENABLED=0
export GOOS=linux
export GOARCH=arm64

# Strip symbols + build paths for smaller, more reproducible binaries.
LDFLAGS="-s -w"

mkdir -p "$OUT"

echo ">> building iec104-client (linux/arm64)"
( cd "$ROOT/iec104_client" && go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/iec104-client" . )

echo ">> done - artifacts:"
ls -l "$OUT"
if command -v file >/dev/null 2>&1; then
	file "$OUT/iec104-client"
fi

