#!/usr/bin/env bash
# Print a health table for every container of the stack.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

printf '%-22s %-12s %s\n' CONTAINER STATE URL
row() { # name url
  local state
  state="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$1" 2>/dev/null || echo absent)"
  printf '%-22s %-12s %s\n' "$1" "$state" "${2:-}"
}

for c in ecom-postgres ecom-mongo ecom-redis ecom-kafka; do row "$c"; done
row ecom-kafka-ui http://localhost:8080
row ecom-mailpit http://localhost:8025
row ecom-grafana http://localhost:3001
row ecom-prometheus http://localhost:9090
for svc in "${SERVICES[@]}"; do
  row "$(container_of "$svc")" "http://localhost:$(port_of "$svc")/health/ready"
done
