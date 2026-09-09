#!/usr/bin/env bash
#
# neon-control-plane 一键构建脚本
#
# 流水线：
#   1) 前置校验 docker / go 是否可用
#   2) 宿主静态编译 neon-control-plane 二进制（CGO_ENABLED=0，linux/amd64，离线无第三方依赖）
#   3) 基于现有 src/Dockerfile 构建镜像（固定打 latest）
#   4) 打 tag 并推送到本地私有仓库 192.168.232.128:5000
#   5) docker image prune -f 仅清理悬空镜像（<none> 中间层 / 旧构建残留）
#
# 用法：
#   ./build.sh
#
# 可通过环境变量覆盖默认配置：
#   REGISTRY=192.168.232.128:5000 REGISTRY_NS=neondatabase IMAGE=neon-control-plane TAG=latest ./build.sh
set -euo pipefail

# ---------------------------------------------------------------------------
# 1. 定位脚本与源码目录
#    SRC_DIR 既是 go build 产物落盘处，也是 docker build 的上下文（context）：
#    src/Dockerfile 中的 `COPY neon-control-plane` 依赖该二进制就在 context 内。
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC_DIR="${SCRIPT_DIR}/src"
BINARY="${SRC_DIR}/neon-control-plane"

# ---------------------------------------------------------------------------
# 2. 可配置项（默认值对齐实际部署的 umbrella chart 镜像地址）
# ---------------------------------------------------------------------------
REGISTRY="${REGISTRY:-192.168.232.128:5000}"    # 本地私有仓库地址
REGISTRY_NS="${REGISTRY_NS:-neondatabase}"      # 镜像命名空间
IMAGE="${IMAGE:-neon-control-plane}"            # 镜像名
TAG="${TAG:-latest}"                            # 固定 latest

# 本地构建用的 tag（不含远端仓库前缀）
LOCAL_TAG="${IMAGE}:${TAG}"
# 远端完整 tag：当 REGISTRY_NS 为空时退化为 ${REGISTRY}/${IMAGE}:${TAG}
if [[ -n "${REGISTRY_NS}" ]]; then
  REMOTE_TAG="${REGISTRY}/${REGISTRY_NS}/${IMAGE}:${TAG}"
else
  REMOTE_TAG="${REGISTRY}/${IMAGE}:${TAG}"
fi

# ---------------------------------------------------------------------------
# 3. 前置校验：docker / go 必须可用
# ---------------------------------------------------------------------------
echo "==> 校验构建依赖"
for cmd in docker go; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "错误：未找到命令 '$cmd'，请先安装并加入 PATH 后再执行本脚本。" >&2
    exit 1
  fi
done
echo "    已就绪：docker=$(command -v docker)，go=$(command -v go)"

# ---------------------------------------------------------------------------
# 4. 静态编译 neon-control-plane
#    - CGO_ENABLED=0：生成纯静态二进制，无需容器内的 C 库依赖
#    - GOOS/GOARCH 固定为 linux/amd64（镜像基础为 alpine:3.20，目标运行平台）
#    - go.mod 零第三方依赖，可完全离线编译，无需 go mod download
# ---------------------------------------------------------------------------
echo "==> [1/4] 编译 neon-control-plane（CGO_ENABLED=0，linux/amd64，离线静态链接）"
# 进入 src 目录执行 go build（go.mod 在 src 内），产物二进制直接写到 SRC_DIR
( cd "$SRC_DIR" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o neon-control-plane . )
echo "    编译完成：${BINARY}"

# ---------------------------------------------------------------------------
# 5. 构建镜像
#    build context 为 SRC_DIR（默认读取其中的 Dockerfile，无需 -f），
#    二进制与 Dockerfile 同处一个目录，COPY 指令可直接命中。
# ---------------------------------------------------------------------------
echo "==> [2/4] 构建镜像（基于 src/Dockerfile，打 ${LOCAL_TAG}）"
docker build -t "${LOCAL_TAG}" "${SRC_DIR}"
echo "    镜像构建完成：${LOCAL_TAG}"

# ---------------------------------------------------------------------------
# 6. 打 tag 并推送到本地私有仓库
#    docker daemon 已配置 insecure-registries 包含该仓库，推送无需 login。
# ---------------------------------------------------------------------------
echo "==> [3/4] 打 tag 并推送到本地仓库 ${REGISTRY}"
docker tag "${LOCAL_TAG}" "${REMOTE_TAG}"
echo "    已打 tag：${REMOTE_TAG}"
docker push "${REMOTE_TAG}"
echo "    推送完成：${REMOTE_TAG}"

# ---------------------------------------------------------------------------
# 7. 清理悬空镜像
#    docker image prune -f（无 -a）：仅删除 <none> 悬空层/旧构建残留，
#    不会触碰带 tag 的 ghcr.io/neondatabase/neon 等有用本地缓存。
# ---------------------------------------------------------------------------
echo "==> [4/4] 清理悬空镜像（docker image prune -f）"
docker image prune -f

echo "完成：neon-control-plane 已构建并推送至 ${REMOTE_TAG}"
