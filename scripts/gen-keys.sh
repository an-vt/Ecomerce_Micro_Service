#!/usr/bin/env bash
# Generate the RS256 key pair used by auth-service (secrets/ is git-ignored).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

mkdir -p "$ROOT/secrets"
if [ -f "$ROOT/secrets/jwt-private.pem" ]; then log "keys already exist"; exit 0; fi
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$ROOT/secrets/jwt-private.pem"
openssl rsa -pubout -in "$ROOT/secrets/jwt-private.pem" -out "$ROOT/secrets/jwt-public.pem"
chmod 644 "$ROOT/secrets/jwt-private.pem" "$ROOT/secrets/jwt-public.pem"   # readable by the non-root container user (local only)
log "wrote secrets/jwt-private.pem and secrets/jwt-public.pem"
