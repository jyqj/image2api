# GPT2API Local 容器化部署

一键启动:

```bash
cd deploy
cp .env.example .env
# 修改 CRYPTO_AES_KEY / MySQL 密码等配置
docker compose up -d --build
docker compose logs -f server
```

Server 启动时会等待 MySQL 健康,如果业务库为空则自动导入 `/app/sql/database.sql`,然后启动 HTTP 服务 `:8080`。

## 默认端口

| 服务 | 端口 | 说明 |
| --- | --- | --- |
| server | `8080` | OpenAI 兼容 `/v1` + 本地控制台 API |
| mysql | `3306` | 业务数据库 |
| redis | `6379` | 锁与缓存 |

## 数据库初始化

初始化 SQL 位于 `sql/database.sql`。容器 entrypoint 判断业务库已有表时会跳过导入,避免重复执行。需要从头开始时,清空 MySQL volume 或删库重建即可。

手动初始化:

```bash
MYSQL_PWD=gpt2api docker compose exec -T mysql mysql -ugpt2api gpt2api < ../sql/database.sql
```

## 数据卷

- `mysql_data`:MySQL 物理数据
- `redis_data`:Redis 数据
- `backups`:`/app/data/backups`,数据库备份文件落盘目录
- `./logs`:宿主机日志目录

## 必改配置

生产或长期自用部署时,请在 `.env` 中覆盖:

- `CRYPTO_AES_KEY`:严格 64 位 hex,用于加密上游账号令牌、cookies 和代理敏感字段
- `MYSQL_ROOT_PASSWORD` / `MYSQL_PASSWORD`

## 备份与恢复

控制台“备份恢复”页可创建、下载、上传和恢复数据库备份。恢复默认关闭,需要:

1. 在 `.env` 中设置 `BACKUP_ALLOW_RESTORE=true`
2. 重启 server
3. 在控制台执行恢复
4. 完成后建议改回 `false` 并重启

## 常用运维命令

```bash
# 进入 MySQL
docker compose exec mysql mysql -ugpt2api -p gpt2api

# 查看业务库表数量
docker compose exec mysql mysql -ugpt2api -p -N -B \
  -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='gpt2api'"

# 冷备份
docker compose exec server mysqldump -hmysql -ugpt2api -p \
  --single-transaction --quick gpt2api | gzip > gpt2api-$(date +%F).sql.gz
```
