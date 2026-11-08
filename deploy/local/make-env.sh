#!/usr/bin/env bash
# Writes .env with random credentials (gitignored). Re-running rotates them: update the repo secrets after.
set -euo pipefail
cd "$(dirname "$0")"
gen() { openssl rand -hex 16; }
reg=$(gen)
hash=$(docker run --rm caddy:2.10.2-alpine caddy hash-password --plaintext "$reg")
umask 077
cat > .env <<ENV
PACT_RW_PASSWORD=$(gen)
PACT_RO_PASSWORD=$(gen)
ARGUS_REGISTRY_PASSWORD=$reg
ARGUS_REGISTRY_PASSWORD_HASH=${hash//\$/\$\$}
ENV
echo "wrote $(pwd)/.env"
