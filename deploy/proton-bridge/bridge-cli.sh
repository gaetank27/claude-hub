#!/usr/bin/env bash
# Interactive Bridge administration (add account, show IMAP password, ...).
# Stops the headless service for the session (Bridge allows one instance), then
# restarts it. Inside the prompt: `login`, `info`, `list`, `exit`.
set -euo pipefail
HERE="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"
. "$HERE/env.sh"
systemctl --user stop proton-bridge.service
trap 'systemctl --user start proton-bridge.service' EXIT
"$PB_BIN" --cli "$@"
