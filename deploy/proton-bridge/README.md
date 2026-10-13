# Proton Bridge (headless) — mail for the assistant

Proton Mail has no public mail API for third parties; the supported route is
**Proton Bridge**, a local IMAP/SMTP gateway. The Hub reads mail through its
generic IMAP connector (`imap_*` tools), pointed at the Bridge on loopback.
(Calendar uses the Proton API directly: connector `proton`, see CONTEXT_MEMORY.)

Everything below is **user space, no root**, and listens on `127.0.0.1` only
(IMAP 1143, SMTP 1025). Tested on Debian 13 / x86_64.

## Install (reproducible, idempotent)

```bash
deploy/proton-bridge/install.sh
```

What it does:

1. Downloads the official `protonmail-bridge_<ver>_amd64.deb` from proton.me,
   verifies its **GPG signature** against Proton's key (fingerprint pinned in the
   script: `D51E 64D3 E63E DC3E EF78  64CE E2C7 5D68 E623 4B07`), and extracts it
   to `~/.local/opt/protonmail-bridge` (no `dpkg -i`).
2. Fetches `libsecret-1-0` and `pass` (+`tree`) from the Debian archive, verified
   through the signed `InRelease` → `Packages.xz` → SHA256 chain, into
   `~/.local/opt/bridge-deps`.
3. Creates the credential vault Bridge needs on a host without a desktop session:
   a **dedicated, passphrase-less GPG key** and a `pass` store under
   `~/.local/share/proton-bridge/{gnupg,password-store}` (mode 0700). It protects
   the Bridge secrets at rest only against other users / backups, not against code
   running as this user — same trust level as the Hub's own `secret.key`.
4. Installs and starts the systemd **user** unit `proton-bridge.service`
   (`Restart=always`, `loginctl enable-linger`), running `bridge --noninteractive`.
5. Links `~/.local/bin/proton-bridge-cli` (admin shell, below).

Bridge data lives in `~/.config/protonmail/bridge-v3` and `~/.local/share/protonmail`.
Update: re-run `install.sh` (it picks the version published in `version_linux.json`).

## Add the Proton account (from the Hub UI)

Open the Hub › **Tools** › panel **Proton Bridge — mail** › *Connect mail (Proton
Bridge)*, then enter the Proton address, password and 2FA code (plus the mailbox
password for two-password accounts). The backend drives `bridge --cli` through a
pty (stops the service, logs in, reads the Bridge IMAP password with `info`,
restarts the service), creates/updates the IMAP tool **Proton Bridge** and opens
it to the assistant. Nothing to type in a terminal; the Proton password and codes
are never stored. This is the step that can trip Proton's anti-abuse
(`Code=2028`): one attempt per click, no automatic retry.

Manual fallback (terminal), if the UI cannot be used:

```bash
proton-bridge-cli          # stops the service for the session, restarts it on exit
>>> login                  # Proton e-mail, password, then the 2FA code if enabled
>>> info                   # shows the Bridge IMAP username + generated password
>>> exit
BRIDGE_PASSWORD='<bridge imap password>' go run ./cmd/addbridge -email you@proton.me
```

The IMAP password shown by `info` is **Bridge's own** (not the Proton password).
The Hub connects with TLS disabled on loopback but upgrades to STARTTLS when Bridge
offers it. The first sync of a large mailbox can take a while; `imap_list_mailboxes`
shows the counts as they fill in. The Hub finds `deploy/proton-bridge` from its
working directory (override: `HUB_PROTON_BRIDGE_DIR`).

## Operate

```bash
systemctl --user status proton-bridge
journalctl --user -u proton-bridge -n 50
ss -ltn | grep -E ':(1143|1025)\b'     # must show 127.0.0.1 only
```
