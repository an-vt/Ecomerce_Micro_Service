#!/usr/bin/env bash
# Scaffold a service module (docs/03-project-structure.md §1.1) from tools/newservice/templates.
#
#   tools/newservice/new-service.sh NAME=order-service SHORT=order HTTP=8106 GRPC=50056 DB=pg
#
# DB: pg | mongo | none.  GRPC=0 means no gRPC server (api-gateway).
# Existing files are never overwritten, so the script is safe to re-run.
set -euo pipefail

for kv in "$@"; do export "$kv"; done
: "${NAME:?NAME required}" "${SHORT:?SHORT required}" "${HTTP:?HTTP required}" "${GRPC:?GRPC required}" "${DB:?DB required}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TPL="$ROOT/tools/newservice/templates"
DIR="$ROOT/$NAME"
DBNAME="${SHORT}_db"

render() { # render <template> <dest>
  local dest="$DIR/$2"
  mkdir -p "$(dirname "$dest")"
  if [ -e "$dest" ]; then echo "skip   $NAME/$2"; return; fi
  local sedexpr=(-e "s|__NAME__|$NAME|g" -e "s|__SHORT__|$SHORT|g" -e "s|__HTTP__|$HTTP|g" -e "s|__GRPC__|$GRPC|g" -e "s|__DBNAME__|$DBNAME|g")
  sed "${sedexpr[@]}" "$TPL/$1" \
    | { if [ "$GRPC" = "0" ]; then sed -e '/__GRPC_ONLY__/d' -e '/^GRPC_PORT=/d'; else sed -e 's| # __GRPC_ONLY__||'; fi; } \
    | { case "$DB" in
          pg)    sed -e '/^MONGO_URI=/d' ;;
          mongo) sed -e '/^DATABASE_URL=/d' ;;
          *)     sed -e '/^MONGO_URI=/d' -e '/^DATABASE_URL=/d' ;;
        esac; } >"$dest"
  echo "create $NAME/$2"
}

doc() { # doc <dir> <package>
  local dest="$DIR/$1/doc.go"
  mkdir -p "$DIR/$1"
  [ -e "$dest" ] && return
  sed -e "s|__PKG__|$2|g" "$TPL/doc.go.tmpl" >"$dest"
}

render go.mod.tmpl go.mod
render Dockerfile.tmpl build/Dockerfile
render docker-compose.yml.tmpl infra/local/docker-compose.yml
render Makefile.tmpl infra/local/Makefile
render env.example.tmpl infra/local/.env.example

for p in domain usecase config app; do doc "internal/$p" "$p"; done
case "$DB" in
  pg)    doc internal/repository/postgres postgres ;;
  mongo) doc internal/repository/mongo mongo ;;
esac
if [ "$GRPC" != "0" ]; then doc internal/transport/grpc grpc; doc internal/transport/kafka kafka; else doc internal/transport/http http; fi
doc internal/client client
doc internal/database/migrations migrations
doc internal/database/seeders seeders

# register the module in go.work
cd "$ROOT"
grep -q "^	\./$NAME\$" go.work || go work use "./$NAME"
