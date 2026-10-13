#!/usr/bin/env bash
# Idempotent install of rrdtool for the host-metrics historisation on the Claude
# Hub host. The backend shells out to the `rrdtool` CLI (no cgo) to create/update
# the fixed-size .rrd database and to export series for the Suivi charts.
#
# Run with sudo:  sudo deploy/rrdtool-setup.sh
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "must run as root (use sudo)" >&2
  exit 1
fi

if command -v rrdtool >/dev/null 2>&1; then
  echo "rrdtool already installed: $(rrdtool --version | head -1)"
  exit 0
fi

echo "installing rrdtool ..."
apt-get update -qq
apt-get install -y rrdtool
echo "done: $(rrdtool --version | head -1)"
