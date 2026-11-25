#!/usr/bin/env bash
#
# install.sh - deploy the built IEC 104 client + systemd unit on
# the Raspberry Pi CM4/CM5 (Debian arm64). Run as root, after ./build.sh.
#
#   ./build.sh
#   sudo ./deploy/install.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PREFIX="/opt/iec104"
SERVICE_USER="iec104"
UNITS_DIR="/etc/systemd/system"
UNITS=("iec104-client.service")

if [[ "$(id -u)" -ne 0 ]]; then
	echo "error: run as root (sudo $0)" >&2
	exit 1
fi

if [[ ! -x "$ROOT/dist/iec104-client" ]]; then
	echo "error: $ROOT/dist/iec104-client missing - run ./build.sh first" >&2
	exit 1
fi

if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
	echo ">> creating system user $SERVICE_USER"
	useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
fi

echo ">> installing binary to $PREFIX"
install -d -o "$SERVICE_USER" -g "$SERVICE_USER" "$PREFIX"
install -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$ROOT/dist/iec104-client" "$PREFIX/iec104-client"

echo ">> installed artifact (compare with $ROOT/dist/ to spot a stale install):"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum "$PREFIX/iec104-client"
fi

echo ">> installing systemd units to $UNITS_DIR"
for u in "${UNITS[@]}"; do
	install -m 0644 "$ROOT/deploy/$u" "$UNITS_DIR/$u"
done

# This repository is client-only now. Earlier deployments installed this
# program as a listening outstation under the old "server" name; that unit and
# binary are removed so nothing competes for the MSL5 session.
echo ">> removing legacy outstation-server unit and binary"
systemctl disable --now iec104-server.service 2>/dev/null || true
rm -f "$UNITS_DIR/iec104-server.service"
rm -f "$PREFIX/iec104-server"

echo ">> reloading systemd and enabling the client"
systemctl daemon-reload
systemctl enable --now iec104-client.service

echo ">> status:"
systemctl --no-pager --full status iec104-client.service || true

echo
echo "done. follow client logs with: journalctl -u iec104-client -f"
echo "dialling MSL5 at 127.0.0.1:2404 (edit /etc/systemd/system/iec104-client.service to change -msl5-host/-msl5-port/-ca)"

