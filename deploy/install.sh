#!/usr/bin/env bash
#
# install.sh - deploy the built IEC 104 outstation + systemd unit on the
# Raspberry Pi CM4/CM5 (Debian arm64). Run as root, after ./build.sh.
#
#   ./build.sh
#   sudo ./deploy/install.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PREFIX="/opt/iec104"
SERVICE_USER="iec104"
UNITS_DIR="/etc/systemd/system"
UNITS=("iec104-server.service")

if [[ "$(id -u)" -ne 0 ]]; then
	echo "error: run as root (sudo $0)" >&2
	exit 1
fi

if [[ ! -x "$ROOT/dist/iec104-server" ]]; then
	echo "error: $ROOT/dist/iec104-server missing - run ./build.sh first" >&2
	exit 1
fi

if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
	echo ">> creating system user $SERVICE_USER"
	useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
fi

echo ">> installing binary to $PREFIX"
install -d -o "$SERVICE_USER" -g "$SERVICE_USER" "$PREFIX"
install -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$ROOT/dist/iec104-server" "$PREFIX/iec104-server"

echo ">> installed artifact (compare with $ROOT/dist/ to spot a stale install):"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum "$PREFIX/iec104-server"
fi

echo ">> installing systemd units to $UNITS_DIR"
for u in "${UNITS[@]}"; do
	install -m 0644 "$ROOT/deploy/$u" "$UNITS_DIR/$u"
done

# This repository is server-only now. Older deployments installed a master
# client, either as a bridge spawned by the backend or as a manual demo unit;
# both are removed so nothing competes for the outstation link.
echo ">> removing legacy master-client units and binary"
for legacy in iec104-client.service iec104-client-demo.service; do
	systemctl disable --now "$legacy" 2>/dev/null || true
	rm -f "$UNITS_DIR/$legacy"
done
rm -f "$PREFIX/iec104-client"

echo ">> reloading systemd and enabling the outstation"
systemctl daemon-reload
systemctl enable --now iec104-server.service

echo ">> status:"
systemctl --no-pager --full status iec104-server.service || true

echo
echo "done. follow outstation logs with: journalctl -u iec104-server -f"
echo "listening on 0.0.0.0:2404; the MSL5 backend's master client connects to it"

