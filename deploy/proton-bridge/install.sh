#!/usr/bin/env bash
# Installs Proton Bridge (headless, user space, no root) for the current user.
#   - Bridge .deb from proton.me, signature checked against Proton's pinned key
#   - libsecret + pass (Bridge's credential vault on a headless host) from the
#     Debian archive, checked through the signed InRelease -> Packages -> SHA256
#   - dedicated GPG key + password store (pass) under ~/.local/share/proton-bridge
#   - systemd user unit (Restart=always), Bridge listening on 127.0.0.1 only
# Idempotent. Does not log in any Proton account (see README.md).
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
PB_OPT="${PB_OPT:-$HOME/.local/opt}"
PB_STATE="${PB_STATE:-$HOME/.local/share/proton-bridge}"
BASE=https://proton.me/download/bridge
PROTON_FPR=D51E64D3E63EDC3EEF7864CEE2C75D68E6234B07 # Proton Bridge developers
DEBIAN=https://deb.debian.org/debian
SUITE="${DEBIAN_SUITE:-trixie}"

WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
mkdir -p "$PB_OPT/protonmail-bridge" "$PB_OPT/bridge-deps" "$PB_STATE" "$HOME/.local/bin"
chmod 700 "$PB_STATE"
export GNUPGHOME_VERIFY="$WORK/gnupg"; mkdir -m700 "$GNUPGHOME_VERIFY"

# --- 1. Bridge .deb -------------------------------------------------------
VERSION="$(curl -sf "$BASE/version_linux.json" | python3 -I -c 'import json,sys;print(json.load(sys.stdin)["stable"]["Version"])')"
DEB="protonmail-bridge_${VERSION}-1_amd64.deb"
if [[ ! -x "$PB_OPT/protonmail-bridge/.version-$VERSION" ]]; then
  (cd "$WORK" && curl -sfLO "$BASE/$DEB" && curl -sfLO "$BASE/$DEB.sig" && curl -sfLO "$BASE/bridge_pubkey.gpg")
  GNUPGHOME="$GNUPGHOME_VERIFY" gpg --batch -q --import "$WORK/bridge_pubkey.gpg"
  GNUPGHOME="$GNUPGHOME_VERIFY" gpg --batch --with-colons --fingerprint | grep -q "^fpr:::::::::$PROTON_FPR:" \
    || { echo "unexpected Proton signing key" >&2; exit 1; }
  GNUPGHOME="$GNUPGHOME_VERIFY" gpg --batch --verify "$WORK/$DEB.sig" "$WORK/$DEB"
  rm -rf "$PB_OPT/protonmail-bridge" && mkdir -p "$PB_OPT/protonmail-bridge"
  dpkg-deb -x "$WORK/$DEB" "$PB_OPT/protonmail-bridge"
  printf '#!/bin/sh\n' > "$PB_OPT/protonmail-bridge/.version-$VERSION"; chmod +x "$PB_OPT/protonmail-bridge/.version-$VERSION"
fi

# --- 2. libsecret + pass from Debian (only what the host lacks) ------------
if [[ ! -x "$PB_OPT/bridge-deps/usr/bin/pass" ]]; then
  cd "$WORK"
  curl -sfO "$DEBIAN/dists/$SUITE/InRelease"
  gpgv --keyring /usr/share/keyrings/debian-archive-keyring.gpg InRelease
  curl -sf "$DEBIAN/dists/$SUITE/main/binary-amd64/Packages.xz" -o Packages.xz
  want="$(awk '/main\/binary-amd64\/Packages.xz$/ {print $1; exit}' InRelease)"
  echo "$want  Packages.xz" | sha256sum -c -
  xz -dc Packages.xz > Packages
  for p in libsecret-1-0 libsecret-common pass tree; do
    read -r file sha < <(awk -v p="$p" 'BEGIN{RS="";FS="\n"} $1=="Package: "p {for(i=1;i<=NF;i++){if($i~/^Filename:/)f=substr($i,11); if($i~/^SHA256:/)s=substr($i,9)}; print f, s}' Packages)
    curl -sfO "$DEBIAN/$file"
    echo "$sha  $(basename "$file")" | sha256sum -c -
    dpkg-deb -x "$(basename "$file")" "$PB_OPT/bridge-deps"
  done
fi

# --- 3. GPG key + pass store (dedicated, passphrase-less, 0700) ----------------
# shellcheck source=env.sh
. "$HERE/env.sh"
if [[ ! -f "$PASSWORD_STORE_DIR/.gpg-id" ]]; then
  mkdir -p -m700 "$GNUPGHOME"
  gpg --batch --pinentry-mode loopback --passphrase '' \
      --quick-generate-key "Proton Bridge (hub) <bridge@localhost>" default default never
  pass init "$(gpg --list-keys --with-colons | awk -F: '/^fpr:/ {print $10; exit}')"
fi

# --- 4. systemd user unit ------------------------------------------------------
mkdir -p "$HOME/.config/systemd/user"
sed "s#@HERE@#$HERE#g; s#@STATE@#$PB_STATE#g" "$HERE/proton-bridge.service" > "$HOME/.config/systemd/user/proton-bridge.service"
ln -sf "$HERE/bridge-cli.sh" "$HOME/.local/bin/proton-bridge-cli"
systemctl --user daemon-reload
systemctl --user enable --now proton-bridge.service
loginctl enable-linger "$USER" 2>/dev/null || true

echo "Proton Bridge $VERSION installed. Add the account with: proton-bridge-cli   (see $HERE/README.md)"
