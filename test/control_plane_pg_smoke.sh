#!/usr/bin/env bash
# 控制面独立库冒烟测试（中文注释）
#
# 前置条件：
#   1. 已按 docs/sql/init_control_plane_db.sql 创建 neon_control_plane 库与独立账号；
#   2. 连接串通过环境变量 NEON_CP_TEST_DATABASE_URL 提供（不得写入脚本或仓库）；
#   3. 严禁指向 storage_controller 库，该库保存 pageserver generation numbers。
#
# 用法：
#   NEON_CP_TEST_DATABASE_URL='postgres://user:pass@host:5432/neon_control_plane' \
#     ./test/control_plane_pg_smoke.sh
set -euo pipefail

: "${NEON_CP_TEST_DATABASE_URL:?请先设置控制面独立库连接串 NEON_CP_TEST_DATABASE_URL}"
export NEON_CP_TEST_DATABASE_URL

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC_DIR="${SCRIPT_DIR}/../charts/neon-control-plane/src"

echo "==> 控制面独立库冒烟：迁移幂等 / 空库加载 / CRUD / 恢复字段"
( cd "$SRC_DIR" && go test -run 'TestPostgres|TestMigration|TestStateStoreContract' -v ./... )
echo "==> 冒烟完成"
