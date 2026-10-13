#!/usr/bin/env bash
# Idempotent rootful Docker setup for the Claude Hub host.
#
# Installs Docker Engine (system service, so `restart=unless-stopped` Live
# containers come back after a host reboot), grants the runtime user access to
# the daemon socket, and creates the two segmented bridge networks the hub uses.
#
# Run with sudo:  sudo deploy/docker-setup.sh [runtime-user]
# Default runtime-user is the invoking user (SUDO_USER) or "claude".
#
# NOTE: membership of the `docker` group is effectively root on the host. This is
# accepted for this single-user homelab. After this script adds the user to the
# group, the systemd --user manager must be restarted (or the box rebooted) for
# the backend to pick up the new group:  sudo systemctl restart user@<uid>
set -euo pipefail

RUNTIME_USER="${1:-${SUDO_USER:-claude}}"

if [[ $EUID -ne 0 ]]; then
  echo "must run as root (use sudo)" >&2
  exit 1
fi

# 1. Install Docker Engine if missing.
if command -v docker >/dev/null 2>&1; then
  echo "docker already installed: $(docker --version)"
else
  echo "installing Docker Engine via get.docker.com ..."
  curl -fsSL https://get.docker.com | sh
fi

# 2. Daemon enabled + started at boot (Live persistence depends on this).
systemctl enable --now docker
echo "docker.service: $(systemctl is-active docker)"

# 3. Socket access for the runtime user.
#    The backend runs as a systemd --user service whose process is long-lived, so
#    adding the user to the `docker` group only takes effect after a user-manager
#    restart / reboot. To grant access to the *already running* backend (checked by
#    uid at connect time) without a disruptive restart, we also lay down an ACL on
#    the socket, re-applied on every daemon (re)start via a drop-in so it survives.
apt-get install -y acl >/dev/null 2>&1 || true

if id -nG "$RUNTIME_USER" | tr ' ' '\n' | grep -qx docker; then
  echo "$RUNTIME_USER already in the docker group"
else
  usermod -aG docker "$RUNTIME_USER"
  echo "added $RUNTIME_USER to the docker group"
fi

DROPIN=/etc/systemd/system/docker.service.d
mkdir -p "$DROPIN"
cat > "$DROPIN/claudehub-acl.conf" <<EOF
[Service]
ExecStartPost=/usr/bin/setfacl -m u:$RUNTIME_USER:rw /var/run/docker.sock
EOF
systemctl daemon-reload
# Apply now to the running daemon (the drop-in covers future restarts).
setfacl -m "u:$RUNTIME_USER:rw" /var/run/docker.sock
echo "granted $RUNTIME_USER rw on the docker socket (ACL + group)"

# 4. Segmented bridge networks (sandbox vs live). `docker network create` is not
#    idempotent, so guard each.
for net in claudehub_sandbox claudehub_live; do
  if docker network inspect "$net" >/dev/null 2>&1; then
    echo "network $net exists"
  else
    docker network create --driver bridge "$net" >/dev/null
    echo "created network $net"
  fi
done

echo "done. uid for the user manager restart: $(id -u "$RUNTIME_USER")"
