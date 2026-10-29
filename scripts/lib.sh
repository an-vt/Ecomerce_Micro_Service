#!/usr/bin/env bash
# Shared helpers for the lifecycle scripts. Source, do not execute.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NETWORK="ecom_net"
SHARED_INFRA="$ROOT/shared-infra/infra/local"

# Start order matters: identity first, the gateway last (docs/11 §4).
SERVICES=(auth-service catalog-service inventory-service cart-service discount-service payment-service order-service notification-service api-gateway)

# service dir -> container name (infra/local/docker-compose.yml container_name)
container_of() {
  case "$1" in
    api-gateway) echo ecom-gateway ;;
    *) echo "ecom-${1%-service}" ;;
  esac
}

# service dir -> ops/health port
port_of() {
  case "$1" in
    api-gateway) echo 8088 ;;
    auth-service) echo 8101 ;;
    catalog-service) echo 8102 ;;
    inventory-service) echo 8103 ;;
    cart-service) echo 8104 ;;
    discount-service) echo 8105 ;;
    order-service) echo 8106 ;;
    payment-service) echo 8107 ;;
    notification-service) echo 8108 ;;
  esac
}

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!\033[0m  %s\n' "$*" >&2; }
die()  { printf '\033[1;31mxx\033[0m  %s\n' "$*" >&2; exit 1; }

require_cmd() { command -v "$1" >/dev/null 2>&1 || die "missing command: $1"; }

ensure_network() {
  docker network inspect "$NETWORK" >/dev/null 2>&1 || {
    log "creating docker network $NETWORK"
    docker network create "$NETWORK" >/dev/null
  }
}

has_infra() { [ -f "$ROOT/$1/infra/local/docker-compose.yml" ]; }

# wait_for_healthy <container> [timeout-seconds]
wait_for_healthy() {
  local name="$1" timeout="${2:-120}" waited=0 state
  while [ "$waited" -lt "$timeout" ]; do
    state="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$name" 2>/dev/null || echo missing)"
    case "$state" in
      healthy|running) return 0 ;;
      unhealthy|exited|dead) die "$name is $state (docker logs $name)" ;;
    esac
    sleep 2; waited=$((waited + 2))
  done
  die "$name not healthy after ${timeout}s"
}
