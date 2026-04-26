#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if [[ $# -ge 1 && -n "${1:-}" ]]; then
  export VMQ_IMAGE="$1"
fi

if [[ ! -f .env ]]; then
  echo ".env 不存在，请先执行 ./scripts/deploy-ghcr.sh 初始化部署。"
  exit 1
fi

echo "Updating image: ${VMQ_IMAGE:-ghcr.io/cvinit/vmq-go:latest}"
docker compose -f docker-compose.ghcr.yml pull
docker compose -f docker-compose.ghcr.yml up -d
docker compose -f docker-compose.ghcr.yml ps
