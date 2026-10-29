#!/usr/bin/env bash
# Runs the end-to-end tests against a throwaway Gitea (or Forgejo) container.
#
#   scripts/e2e-gitea.sh                       # gitea/gitea:1.24
#   IMAGE=codeberg.org/forgejo/forgejo:11 scripts/e2e-gitea.sh
set -euo pipefail

IMAGE="${IMAGE:-gitea/gitea:1.24}"
PORT="${PORT:-3300}"
NAME="publish-please-e2e-$$"
URL="http://localhost:${PORT}"

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker run -d --name "$NAME" -p "${PORT}:3000" \
  -e GITEA__security__INSTALL_LOCK=true \
  -e GITEA__server__ROOT_URL="${URL}/" \
  -e GITEA__database__DB_TYPE=sqlite3 \
  -e FORGEJO__security__INSTALL_LOCK=true \
  -e FORGEJO__server__ROOT_URL="${URL}/" \
  -e FORGEJO__database__DB_TYPE=sqlite3 \
  "$IMAGE" >/dev/null

echo "waiting for ${IMAGE} on ${URL}..."
for _ in $(seq 1 90); do
  curl -sf "${URL}/api/v1/version" >/dev/null && break
  sleep 2
done
curl -sf "${URL}/api/v1/version" >/dev/null || { docker logs "$NAME"; exit 1; }

BIN=gitea
docker exec "$NAME" sh -c 'command -v forgejo' >/dev/null 2>&1 && BIN=forgejo
docker exec -u git "$NAME" "$BIN" admin user create \
  --username pp --password 'pp-pass-1234' --email pp@example.com --admin --must-change-password=false >/dev/null

TOKEN=$(curl -sf -u pp:pp-pass-1234 -H 'Content-Type: application/json' \
  -X POST "${URL}/api/v1/users/pp/tokens" -d '{"name":"e2e","scopes":["all"]}' |
  sed -n 's/.*"sha1":"\([^"]*\)".*/\1/p')

PP_E2E_URL="$URL" PP_E2E_USER=pp PP_E2E_TOKEN="$TOKEN" \
  go test -tags e2e -count=1 -v ./test/e2e/ "$@"
