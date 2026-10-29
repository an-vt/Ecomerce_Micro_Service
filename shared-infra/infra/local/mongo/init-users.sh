#!/usr/bin/env bash
# One login per MongoDB service with readWrite on its own database only (docs/11 §1).
# Runs once, on first start, from /docker-entrypoint-initdb.d.
set -euo pipefail

create() { # <name> <password>
  mongosh --quiet -u "$MONGO_INITDB_ROOT_USERNAME" -p "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin <<JS
db = db.getSiblingDB('$1_db');
db.createUser({ user: '$1', pwd: '$2', roles: [{ role: 'readWrite', db: '$1_db' }] });
JS
}

create catalog      "${CATALOG_DB_PASSWORD:?}"
create cart         "${CART_DB_PASSWORD:?}"
create notification "${NOTIFICATION_DB_PASSWORD:?}"
