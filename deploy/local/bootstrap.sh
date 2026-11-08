#!/usr/bin/env bash
# Sets Apicurio global rules (docs/04 section 7) and Pact Broker environments (docs/05). Idempotent.
set -euo pipefail
R=${ARGUS_REGISTRY_URL:-http://localhost:8080/apis/registry/v3}
B=${PACT_BROKER_BASE_URL:-http://localhost:9292}
rw=$(grep '^PACT_RW_PASSWORD=' "$(dirname "$0")/.env" | cut -d= -f2)
AUTH=(-u "${PACT_BROKER_USERNAME:-argus_rw}:${PACT_BROKER_PASSWORD:-$rw}")

until curl -fsS -o /dev/null "$R/system/info"; do echo "waiting for apicurio"; sleep 3; done
until curl -fsS -o /dev/null "$B/diagnostic/status/heartbeat"; do echo "waiting for pact broker"; sleep 3; done

for rule in VALIDITY:FULL COMPATIBILITY:NONE INTEGRITY:FULL; do
  t=${rule%%:*}; c=${rule##*:}
  curl -fsS -o /dev/null -X POST "$R/admin/rules" -H 'Content-Type: application/json' -d "{\"ruleType\":\"$t\",\"config\":\"$c\"}" \
    || curl -fsS -o /dev/null -X PUT "$R/admin/rules/$t" -H 'Content-Type: application/json' -d "{\"ruleType\":\"$t\",\"config\":\"$c\"}"
  echo "apicurio rule $t=$c"
done

existing=$(curl -fsS "${AUTH[@]}" "$B/environments")
for env in staging production; do
  if grep -q "\"name\":\"$env\"" <<<"$existing"; then echo "broker env $env exists"; continue; fi
  prod=false; [ "$env" = production ] && prod=true
  curl -fsS -o /dev/null "${AUTH[@]}" -X POST "$B/environments" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$env\",\"displayName\":\"$env\",\"production\":$prod}"
  echo "broker env $env created"
done
