#!/usr/bin/env bash
#
# uninstall.sh - stop/disable and remove the IEC 104 client (outstation)
# service + binary. Run as root:
#
#   sudo ./deploy/uninstall.sh
set -euo pipefail

PREFIX="/opt/iec104"
UNITS_DIR="/etc/systemd/system"
# The old "iec104-server" unit is listed too: earlier deployments of this
# repository installed a listening outstation under that name, so uninstall
# also cleans it up.
UNITS=("iec104-client.service" "iec104-server.service")

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
