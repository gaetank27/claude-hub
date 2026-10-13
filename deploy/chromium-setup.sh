#!/usr/bin/env bash
# Idempotent setup for the headless browser used by cmd/docgen to capture UI
# screenshots and compile the PDF manual. Chromium is the single dependency and
# serves both jobs (DevTools CaptureScreenshot + PrintToPDF). It is only invoked
# on demand by docgen, never by the always-on backend, so the running service
# stays lightweight.
#
# Usage: deploy/chromium-setup.sh
set -euo pipefail

if command -v chromium >/dev/null 2>&1; then
  echo "chromium already installed: $(chromium --version)"
  exit 0
fi
if command -v chromium-browser >/dev/null 2>&1; then
  echo "chromium-browser already installed: $(chromium-browser --version)"
  exit 0
fi

echo "installing chromium via apt…"
sudo apt-get update -qq
sudo apt-get install -y chromium

echo "done: $(chromium --version)"
