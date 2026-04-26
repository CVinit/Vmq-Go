#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if [[ $# -ge 1 && -n "${1:-}" ]]; then
  export VMQ_IMAGE="$1"
fi

if [[ ! -f .env ]]; then
  cp .env.example .env
  cat <<'EOF'
.env 已从 .env.example 生成。
请先编辑 .env，至少修改：
- SESSION_SECRET
- ADMIN_USER
- ADMIN_PASS
- POSTGRES_PASSWORD

修改完成后重新执行：
  ./scripts/deploy-ghcr.sh
EOF
  exit 1
fi

echo "Using image: ${VMQ_IMAGE:-ghcr.io/cvinit/vmq-go:latest}"
docker compose -f docker-compose.ghcr.yml pull
docker compose -f docker-compose.ghcr.yml up -d
docker compose -f docker-compose.ghcr.yml ps
