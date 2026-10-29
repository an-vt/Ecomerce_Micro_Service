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

# promtool comes from the same image the stack runs, so what is checked is what will load
rules="$SHARED_INFRA/observability/prometheus/rules"
prom_image="$(awk '/image: prom\/prometheus/ {print $2; exit}' "$SHARED_INFRA/docker-compose.yml")"
rule_files=()
for f in "$rules"/*.yml; do [ -e "$f" ] && rule_files+=("/rules/$(basename "$f")"); done # the container has no shell to expand a glob
if [ ${#rule_files[@]} -gt 0 ]; then
  docker run --rm --entrypoint promtool -v "$rules:/rules:ro" "$prom_image" check rules "${rule_files[@]}"
  log "prometheus rules ok ($prom_image)"
fi
prom_cfg="$SHARED_INFRA/observability/prometheus"
docker run --rm --entrypoint promtool -v "$prom_cfg:/etc/prometheus:ro" "$prom_image" check config /etc/prometheus/prometheus.yml
docker run --rm --entrypoint amtool -v "$SHARED_INFRA/observability/alertmanager:/cfg:ro" "$(awk '/image: prom\/alertmanager/ {print $2; exit}' "$SHARED_INFRA/docker-compose.yml")" check-config /cfg/alertmanager.yml
log "prometheus and alertmanager configuration ok"

# the committed dashboards are what the generator produces (nobody edited the JSON by hand and forgot the source)
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
DASHBOARDS_OUT="$tmp" python3 "$SHARED_INFRA/observability/grafana/gen/dashboards.py" >/dev/null
diff -r "$tmp" "$SHARED_INFRA/observability/grafana/dashboards" >/dev/null || die "dashboards are out of date: run make dashboards"
log "dashboards match their generator"

# dashboards and rules may only use metrics that something really exports; the checker has its own tests
python3 -m unittest "$ROOT/scripts/test_check_observability.py"
python3 "$ROOT/scripts/check-observability.py"
