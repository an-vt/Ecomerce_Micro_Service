#!/usr/bin/env bash
# Stop services in reverse order, then shared infra.  --volumes also deletes data volumes.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

VOL=""
[ "${1:-}" = "--volumes" ] && VOL="-v"

for ((i=${#SERVICES[@]}-1; i>=0; i--)); do
  svc="${SERVICES[$i]}"
  has_infra "$svc" || continue
  log "stopping $svc"
  (cd "$ROOT/$svc/infra/local" && { [ -f .env ] || cp .env.example .env; } && docker compose down $VOL) || warn "$svc: already down"
done

log "stopping shared infra"
(cd "$SHARED_INFRA" && { [ -f .env ] || cp .env.example .env; } && docker compose --profile observability down $VOL)
