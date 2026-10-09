#!/usr/bin/env bash
#
# install.sh - deploy the built IEC 104 binaries + systemd units on the
# Raspberry Pi CM5 (Debian arm64). Run as root, after ./build.sh.
#
# iec104-client (built from iec104_app, the hardware-layer application) is\n# run by the demo unit below. Usage:
#
#   ./build.sh
#   sudo ./deploy/install.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PREFIX="/opt/iec104"
SERVICE_USER="iec104"
UNITS_DIR="/etc/systemd/system"
UNITS=("iec104-server.service" "iec104-client-demo.service")

if [[ "$(id -u)" -ne 0 ]]; then
	echo "error: run as root (sudo $0)" >&2
	exit 1
fi

for b in iec104-server iec104-client; do
	if [[ ! -x "$ROOT/dist/$b" ]]; then
		echo "error: $ROOT/dist/$b missing - run ./build.sh first" >&2
		exit 1
	fi
done

if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
	echo ">> creating system user $SERVICE_USER"
	useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
fi

echo ">> installing binaries to $PREFIX"
install -d -o "$SERVICE_USER" -g "$SERVICE_USER" "$PREFIX"
install -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$ROOT/dist/iec104-server" "$PREFIX/iec104-server"
install -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$ROOT/dist/iec104-client" "$PREFIX/iec104-client"

echo ">> installed artifacts (compare with $ROOT/dist/ to spot a stale install):"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum "$PREFIX/iec104-server" "$PREFIX/iec104-client"
fi

echo ">> installing systemd units to $UNITS_DIR"
for u in "${UNITS[@]}"; do
	install -m 0644 "$ROOT/deploy/$u" "$UNITS_DIR/$u"
done

echo ">> removing the legacy iec104-client.service (the backend spawns the client now)"
systemctl disable --now iec104-client.service 2>/dev/null || true
rm -f "$UNITS_DIR/iec104-client.service"

echo ">> reloading systemd and enabling services"
systemctl daemon-reload
# Only the outstation is enabled at boot. The demo unit
# is manual only (it has no [Install] section), so make sure an earlier deploy
# cannot have left it enabled, and leave it stopped.
systemctl enable --now iec104-server.service
systemctl disable --now iec104-client-demo.service 2>/dev/null || true

echo ">> status:"
systemctl --no-pager --full status iec104-server.service || true

echo
echo "done. follow outstation logs with: journalctl -u iec104-server -f"
echo "client binary: $PREFIX/iec104-client"
echo "demo mode: sudo systemctl start iec104-client-demo   (stop the backend first: only one master per outstation)"
