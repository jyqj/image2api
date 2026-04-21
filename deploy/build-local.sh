#!/usr/bin/env bash
# Linux 本地预构建脚本(服务器上直接用 / WSL / macOS 均可)
#
# 用法:
#   bash deploy/build-local.sh
#
# 产物:
#   deploy/bin/gpt2api        linux/amd64 可执行(后端)
#   web/dist/                 前端 Vite 产物
#
# 这套产物 + deploy/Dockerfile 就可以离线构建镜像,无需容器再访问外网。

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

echo "[build-local] repo  = $ROOT"

# ---- step1: 交叉编译 gpt2api ----
echo "[build-local] step1 = cross-build gpt2api (linux/amd64)"
mkdir -p deploy/bin
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
    go build -ldflags "-s -w" -o deploy/bin/gpt2api ./cmd/server

# ---- step2: 前端 ----
echo "[build-local] step2 = npm run build (web)"
pushd web >/dev/null
if [ ! -d node_modules ]; then
    npm install --no-audit --no-fund --loglevel=error
fi
npm run build
popd >/dev/null

echo "[build-local] done. artifacts:"
ls -lh deploy/bin/gpt2api web/dist/index.html
