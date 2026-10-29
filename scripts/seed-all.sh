#!/usr/bin/env bash
# Run seeders of the services that have one (auth: roles, admin, dev API key).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

for svc in "${SERVICES[@]}"; do
  [ -d "$ROOT/$svc/internal/database/seeders" ] && ls "$ROOT/$svc/internal/database/seeders"/*Seeder.go >/dev/null 2>&1 || continue
  log "seeding $svc"
  make -C "$ROOT/$svc/infra/local" seed
done
