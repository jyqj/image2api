# GPT2API Local

GPT2API Local 是一个自用 OpenAI 兼容中转。它把本地 `/v1` 请求转发到 chatgpt.com 上游账号池,保留账号池、代理池、模型映射、图片任务、用量观察、审计日志、备份恢复和系统设置。


## 功能范围

- OpenAI 兼容接口:`/v1/models`、`/v1/chat/completions`、`/v1/images/generations`、`/v1/images/edits`。
- 上游账号池:AT/RT/ST 导入、自动刷新、状态标记、图片剩余量探测。
- 代理池:HTTP/SOCKS 代理管理、探测、账号绑定。
- 模型映射:对外 `model` slug 映射到 chatgpt.com 上游模型名,并控制是否开放。
- 用量观察:请求数、成功率、token、图片数、耗时、最近错误。
- 图片任务:同步结果与任务记录,图片 URL 通过本地签名代理输出。
- 运维能力:审计日志、数据库备份/恢复、系统设置、SMTP 测试邮件。

## 快速启动

```bash
cp configs/config.example.yaml configs/config.yaml
# 按需修改 MySQL/Redis/AES/SMTP 等配置
go run ./cmd/server -c configs/config.yaml
```

前端开发:

```bash
cd web
npm install
npm run dev
```

Docker 启动:

```bash
cd deploy
cp .env.example .env
docker compose up -d --build
```

首次空库启动时,容器入口会导入 `sql/database.sql`。

## 本地调用示例

```bash
curl http://localhost:8080/v1/models
```

```bash
curl http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"stream":true}'
```

```bash
curl http://localhost:8080/v1/images/generations \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-image-2","prompt":"a cat reading a book","n":1,"size":"1024x1024"}'
```

## 数据表

`sql/database.sql` 只保留本地中转需要的表:

- `oai_accounts` / `oai_account_cookies` / `account_proxy_bindings`
- `proxies`
- `models`
- `usage_logs`
- `image_tasks`
- `system_settings`
- `admin_audit_logs`
- `backup_files`

`usage_logs` 和 `image_tasks` 只记录模型、上游账号、请求状态、耗时和结果信息,不绑定外部分发身份。

## 控制台页面

- `/personal/dashboard`:本地总览
- `/personal/play`:在线体验
- `/personal/usage`:用量记录
- `/personal/docs`:接口文档
- `/admin/accounts`:上游账号池
- `/admin/proxies`:代理池
- `/admin/models`:模型映射
- `/admin/usage`:全局用量
- `/admin/audit`:审计日志
- `/admin/backup`:备份恢复
- `/admin/settings`:系统设置

## 配置

主要配置位于 `configs/config.yaml`:

- `mysql.dsn`:MySQL 连接串
- `redis.addr`:Redis 地址
- `crypto.aes_key`:64 位十六进制 AES-256-GCM 密钥,用于加密上游账号令牌、cookies 和代理敏感字段
- `upstream.base_url`:默认 `https://chatgpt.com`
- `scheduler.*`:账号调度、冷却和使用比例
- `backup.*`:备份目录、保留数量、是否允许恢复
- `smtp.*`:测试邮件发送配置

## 目录结构

```text
cmd/server/          服务入口
internal/account/    上游账号池
internal/proxy/      代理池
internal/model/      模型映射
internal/gateway/    OpenAI 兼容网关
internal/image/      图片任务与结果代理
internal/usage/      用量观察
internal/settings/   系统设置
internal/backup/     备份恢复
internal/audit/      审计日志
web/                 Vue 控制台
sql/database.sql     初始化表结构
```
