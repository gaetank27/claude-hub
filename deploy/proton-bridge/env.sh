# Sourced by the Proton Bridge service and the helper scripts: environment of the
# user-space install made by install.sh. No secret lives here.
PB_OPT="${PB_OPT:-$HOME/.local/opt}"
PB_STATE="${PB_STATE:-$HOME/.local/share/proton-bridge}"
export PB_BIN="$PB_OPT/protonmail-bridge/usr/lib/protonmail/bridge/bridge"
export PATH="$PB_OPT/bridge-deps/usr/bin:$PATH"
export LD_LIBRARY_PATH="$PB_OPT/bridge-deps/usr/lib/x86_64-linux-gnu${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
# Credential vault: `pass` over a dedicated, passphrase-less GPG key (no desktop
# session / Secret Service on this headless host).
export GNUPGHOME="$PB_STATE/gnupg"
export PASSWORD_STORE_DIR="$PB_STATE/password-store"
