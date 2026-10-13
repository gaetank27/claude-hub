#!/usr/bin/env bash
# Re-applies the status.example.com and mcp.example.com routes into the running Caddy via
# the admin API (they are NOT in /etc/caddy/Caddyfile, which needs root). Idempotent:
# an existing route for the same host is removed first. Run after every Caddy restart.
set -euo pipefail
ADMIN=${CADDY_ADMIN:-http://localhost:2019}
SRV="$ADMIN/config/apps/http/servers/srv0/routes"
DIR=$(cd "$(dirname "$0")" && pwd)
for f in status mcp; do
  host="$f.example.com"
  # delete every existing route matching this host (highest index first)
  for i in $(curl -s "$SRV" | python3 -c "
import json,sys
r=json.load(sys.stdin)
print(' '.join(str(i) for i in reversed(range(len(r))) if any(host in m.get('host',[]) for m in r[i].get('match',[]) for host in ['$host'])))"); do
    curl -s -o /dev/null -X DELETE "$SRV/$i"
  done
  # PUT on an array index INSERTS; position 0 so these host-specific routes come first
  curl -sf -o /dev/null -X PUT -H 'Content-Type: application/json' --data @"$DIR/$f-route.json" "$SRV/0"
  echo "applied $host"
done
