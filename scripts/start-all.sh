#!/usr/bin/env bash
# Bring the whole system up: shared infra -> services (in dependency order) -> seed -> status.
# Services whose infra/local folder does not exist yet are skipped, so this works phase by phase.
#   OBSERVABILITY=0 ./scripts/start-all.sh    # skip Prometheus/Grafana/Loki/Tempo
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

require_cmd docker
ensure_network

[ -f "$ROOT/secrets/jwt-private.pem" ] || "$ROOT/scripts/gen-keys.sh"

log "shared infra"
make -C "$SHARED_INFRA" up OBSERVABILITY="${OBSERVABILITY:-1}"

for svc in "${SERVICES[@]}"; do
  if ! has_infra "$svc" || [ ! -f "$ROOT/$svc/cmd/$svc/main.go" ]; then
    warn "skipping $svc (not implemented yet)"
    continue
  fi
  log "$svc"
  make -C "$ROOT/$svc/infra/local" up
  wait_for_healthy "$(container_of "$svc")"
done

"$ROOT/scripts/seed-all.sh"
"$ROOT/scripts/status.sh"
