#!/usr/bin/env bash
# Run migrations of the PostgreSQL services (they also run on boot; this is for manual runs).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

for svc in "${SERVICES[@]}"; do
  ls "$ROOT/$svc/internal/database/migrations"/*_*.go >/dev/null 2>&1 || continue
  log "migrating $svc"
  make -C "$ROOT/$svc/infra/local" migrate
done
