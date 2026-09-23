#!/usr/bin/env bash
set -euo pipefail
export GOCACHE="${GOCACHE:-/tmp/saveknot-go-build}"

# Exercise SaveKnot's S3/R2 wire protocol and two independent local devices.
# Wrangler's local R2 binding has no S3 API endpoint, so use MinIO here.
image='quay.io/minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e'
container_name="saveknot-r2-e2e-$$"
access_key='saveknot-e2e'
secret_key='saveknot-e2e-secret-key'

cleanup() {
  docker rm -f "$container_name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --rm --name "$container_name" \
  -e "MINIO_ROOT_USER=$access_key" -e "MINIO_ROOT_PASSWORD=$secret_key" \
  -p 127.0.0.1::9000 "$image" server /data >/dev/null

port="$(docker port "$container_name" 9000/tcp | sed -E 's/.*:([0-9]+)$/\1/')"
endpoint="http://127.0.0.1:$port"
ready=0
for _ in {1..40}; do
  if curl -fsS "$endpoint/minio/health/live" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.25
done
if [[ "$ready" != 1 ]]; then
  docker logs "$container_name" >&2
  echo 'MinIO did not become ready' >&2
  exit 1
fi

SAVEKNOT_E2E_S3_ENDPOINT="$endpoint" \
SAVEKNOT_E2E_R2_BUCKET='saveknot-e2e' \
SAVEKNOT_E2E_R2_ACCESS_KEY_ID="$access_key" \
SAVEKNOT_E2E_R2_SECRET_ACCESS_KEY="$secret_key" \
go test ./internal/remote -run '^TestR2TwoDevicesEndToEnd$' -count=1 -v
