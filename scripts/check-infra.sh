#!/usr/bin/env bash
# Validate every compose file and the Prometheus rules (docs/PLAN.md P12).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

check_compose() { # dir
  local d="$1" tmp=0
  [ -f "$d/.env" ] || { cp "$d/.env.example" "$d/.env"; tmp=1; }
  docker compose -f "$d/docker-compose.yml" --env-file "$d/.env" --profile observability config -q && log "compose ok: ${d#$ROOT/}"
  [ $tmp -eq 1 ] && rm -f "$d/.env"
  return 0
}

check_compose "$SHARED_INFRA"
for svc in "${SERVICES[@]}"; do has_infra "$svc" && check_compose "$ROOT/$svc/infra/local"; done

rules="$SHARED_INFRA/observability/prometheus/rules"
if ls "$rules"/*.yml >/dev/null 2>&1; then
  docker run --rm --entrypoint promtool -v "$rules:/rules:ro" prom/prometheus:v3.5.0 check rules /rules/*.yml
fi
