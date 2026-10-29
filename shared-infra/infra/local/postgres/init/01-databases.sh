#!/usr/bin/env bash
# One database + one login per PostgreSQL service; each login owns only its own database (docs/11 §1).
# Identifiers are quoted: "order" is a reserved word.
set -euo pipefail

create() { # <name> <password>
  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres <<SQL
DO \$\$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '$1') THEN
    CREATE ROLE "$1" LOGIN PASSWORD '$2';
  END IF;
END
\$\$;
SELECT 'CREATE DATABASE "$1_db" OWNER "$1" ENCODING ''UTF8''' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '$1_db')\gexec
REVOKE ALL ON DATABASE "$1_db" FROM PUBLIC;
SQL
}

create auth      "${AUTH_DB_PASSWORD:?}"
create inventory "${INVENTORY_DB_PASSWORD:?}"
create discount  "${DISCOUNT_DB_PASSWORD:?}"
create order     "${ORDER_DB_PASSWORD:?}"
create payment   "${PAYMENT_DB_PASSWORD:?}"
