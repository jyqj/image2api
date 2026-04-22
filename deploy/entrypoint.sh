#!/usr/bin/env bash
# gpt2api 容器启动入口。
#
# 职责:
#   1. 等待 MySQL 可连接(最多 60 秒)
#   2. 空库时导入 /app/sql/database.sql
#   3. exec 启动 server 主进程
#
# 读取的环境变量:
#   - MYSQL_HOST        (默认 mysql)
#   - MYSQL_PORT        (默认 3306)
#   - MYSQL_USER        (默认 gpt2api)
#   - MYSQL_PASSWORD    (默认 gpt2api)
#   - MYSQL_DATABASE    (默认 gpt2api)
#   - SKIP_DB_INIT=1    跳过自动初始化
set -euo pipefail

MYSQL_HOST=${MYSQL_HOST:-mysql}
MYSQL_PORT=${MYSQL_PORT:-3306}
MYSQL_USER=${MYSQL_USER:-gpt2api}
MYSQL_PASSWORD=${MYSQL_PASSWORD:-gpt2api}
MYSQL_DATABASE=${MYSQL_DATABASE:-gpt2api}

log() { echo "[entrypoint] $*"; }

wait_mysql() {
  log "waiting for mysql ${MYSQL_HOST}:${MYSQL_PORT}..."
  local i=0
  while (( i < 60 )); do
    if MYSQL_PWD="${MYSQL_PASSWORD}" mysqladmin ping \
        -h "${MYSQL_HOST}" -P "${MYSQL_PORT}" -u "${MYSQL_USER}" --silent 2>/dev/null; then
      log "mysql is up."
      return 0
    fi
    sleep 1
    i=$((i+1))
  done
  log "mysql did not become ready in 60s, continuing anyway."
  return 1
}

run_db_init() {
  if [[ "${SKIP_DB_INIT:-0}" == "1" ]]; then
    log "SKIP_DB_INIT=1, skipping database initialization"
    return 0
  fi

  local table_count
  table_count=$(MYSQL_PWD="${MYSQL_PASSWORD}" mysql \
    -h "${MYSQL_HOST}" -P "${MYSQL_PORT}" -u "${MYSQL_USER}" \
    -N -B -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='${MYSQL_DATABASE}'" 2>/dev/null || echo "0")

  if [[ "${table_count}" != "0" ]]; then
    log "database ${MYSQL_DATABASE} already has ${table_count} table(s), skip sql/database.sql"
    return 0
  fi

  log "database ${MYSQL_DATABASE} is empty, importing /app/sql/database.sql..."
  MYSQL_PWD="${MYSQL_PASSWORD}" mysql \
    -h "${MYSQL_HOST}" -P "${MYSQL_PORT}" -u "${MYSQL_USER}" "${MYSQL_DATABASE}" \
    < /app/sql/database.sql
  log "database initialization done."
}

run_db_migrate() {
  # 增量迁移:检查并添加 image_tasks 表的新字段(幂等)
  run_sql() {
    MYSQL_PWD="${MYSQL_PASSWORD}" mysql \
      -h "${MYSQL_HOST}" -P "${MYSQL_PORT}" -u "${MYSQL_USER}" "${MYSQL_DATABASE}" \
      -N -B -e "$1" 2>/dev/null
  }

  col_exists() {
    local cnt
    cnt=$(run_sql "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='${MYSQL_DATABASE}' AND table_name='$1' AND column_name='$2'" || echo "0")
    [[ "${cnt}" != "0" ]]
  }

  add_col() {
    local tbl=$1 col=$2 ddl=$3
    if ! col_exists "${tbl}" "${col}"; then
      log "adding column ${tbl}.${col}"
      run_sql "ALTER TABLE ${tbl} ADD COLUMN ${ddl}" || log "  WARN: ALTER failed for ${col}"
    fi
  }

  add_col image_tasks revised_prompt  "revised_prompt TEXT NULL AFTER prompt"
  add_col image_tasks quality         "quality VARCHAR(32) NOT NULL DEFAULT '' AFTER size"
  add_col image_tasks style           "style VARCHAR(32) NOT NULL DEFAULT '' AFTER quality"
  add_col image_tasks reference_urls  "reference_urls JSON NULL AFTER result_urls"
  add_col image_tasks attempts        "attempts INT NOT NULL DEFAULT 0 AFTER error"
  add_col image_tasks duration_ms     "duration_ms BIGINT NOT NULL DEFAULT 0 AFTER attempts"
  add_col image_tasks user_id         "user_id VARCHAR(128) NOT NULL DEFAULT '' AFTER duration_ms"
}

wait_mysql || true
run_db_init || { log "database initialization failed"; exit 1; }
run_db_migrate || { log "database migration failed (non-fatal)"; }

log "starting: $*"
exec "$@"
