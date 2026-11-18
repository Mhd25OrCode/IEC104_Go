#!/usr/bin/env bash
#
# uninstall.sh - stop/disable and remove the IEC 104 outstation service + binary.
# Run as root:
#
#   sudo ./deploy/uninstall.sh
set -euo pipefail

PREFIX="/opt/iec104"
UNITS_DIR="/etc/systemd/system"
# The master-client units are listed too: older deployments installed them and
# this repository is server-only now, so uninstall also cleans the legacy units.
UNITS=("iec104-server.service" "iec104-client.service" "iec104-client-demo.service")

if [[ "$(id -u)" -ne 0 ]]; then
	echo "error: run as root (sudo $0)" >&2
	exit 1
fi

echo ">> stopping + disabling services"
systemctl disable --now "${UNITS[@]}" 2>/dev/null || true

echo ">> removing unit files"
for u in "${UNITS[@]}"; do
	rm -f "$UNITS_DIR/$u"
done
systemctl daemon-reload

echo ">> removing $PREFIX"
rm -rf "$PREFIX"

echo "done."
