#!/usr/bin/env bash
# Stop the two local clouds started by dev-multicloud-up.sh and drop their DBs.
set -uo pipefail
RUN="/tmp/omnia-mc"
for alias in work personal; do
  if [[ -f "$RUN/$alias.pid" ]]; then
    kill "$(cat "$RUN/$alias.pid")" 2>/dev/null && echo "stopped $alias" || true
    rm -f "$RUN/$alias.pid"
  fi
done
dropdb --if-exists engram_cloud_work 2>/dev/null || true
dropdb --if-exists engram_cloud_personal 2>/dev/null || true
echo "clouds down, dbs dropped"
