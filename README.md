# GPT Image 2API

将 ChatGPT 网页端的图像生成能力（GPT-Image-2 / IMG2）包装为标准 OpenAI 兼容 API，支持文生图、图生图、多模态聊天生图，自带账号池调度、代理池、管理后台。

## 核心能力

- **OpenAI 兼容接口**
  - `POST /v1/images/generations` — 文生图 / 图生图
  - `POST /v1/images/edits` — 图片编辑
  - `POST /v1/chat/completions` — 多模态聊天生图（model 为图像模型时自动走图片链路，支持 stream）
  - `GET /v1/models` — 模型列表
  - `GET /v1/images/tasks/:id` — 异步任务查询

- **图像生成**
  - IMG2 高清终稿（非 IMG1 预览），自动灰度检测 + 换号重试
  - 支持 `reference_images` / `image_url` / `input_image` 等多种参考图格式
  - 支持 base64 / data URL / HTTP URL 参考图输入
  - 图片通过本地签名代理输出（`/p/img/...`），estuary 永久 URL 落库
  - 训练数据留存：prompt、revised_prompt、quality、style、参考图 GPT file ID、生成结果 URL、耗时、重试次数全部入库

- **账号池**
  - 支持 AT / RT / ST / Session JSON 四种导入方式
  - RT→AT 自动刷新，JWT 自动解析 subscription_type（pro/plus/free/team）
  - 轮询调度 + 10s 缓存池，避免并发选中同一账号
  - quota 预检、preview_only 自动换号、失败降低置信度

- **代理池**
  - HTTP / HTTPS / SOCKS5 代理管理
  - 支持 `host:port:user:pass` / `user:pass:host:port` 等多种代理商格式导入
  - 导入后自动探测（≤50 条同步，>50 条后台队列）
  - 探测目标：`chatgpt.com/cdn-cgi/trace`（直接验证 ChatGPT 可达性）

- **管理后台**
  - 登录认证（JWT，密钥从 AES key 派生，重启不失效）
  - 可配置 API Key（`/v1/*` 路由鉴权）
  - 运维仪表盘：账号池/代理池状态概览、每日用量柱状图
  - 在线体验：文生图 + 图生图（最多 4 张参考图）
  - 系统设置、审计日志、数据库备份恢复

## 快速开始

### Docker 部署（推荐）

```bash
cd deploy
cp .env.example .env
# 编辑 .env: 修改 MYSQL 密码、CRYPTO_AES_KEY、HTTP_PORT 等
docker compose up -d --build
```

首次空库启动时，容器入口自动导入 `sql/database.sql` 并执行增量迁移。

### 本地开发

```bash
# 后端
cp configs/config.example.yaml configs/config.yaml
go run ./cmd/server -c configs/config.yaml

# 前端
cd web && npm install && npm run dev
```

## API 调用示例

所有 `/v1/*` 接口需要 `Authorization: Bearer <api_key>`（在管理后台 → 系统设置 → API 认证中配置）。

### 文生图

```bash
curl http://YOUR_HOST/v1/images/generations \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk-your-key' \
  -d '{
    "model": "gpt-image-2",
    "prompt": "a cat reading a book in a cozy library",
    "size": "1024x1024",
    "quality": "high"
  }'
```

### 图生图（参考图）

```bash
curl http://YOUR_HOST/v1/images/generations \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk-your-key' \
  -d '{
    "model": "gpt-image-2",
    "prompt": "make it in watercolor style",
    "reference_images": ["https://example.com/photo.jpg"]
  }'
```

### 多模态聊天生图（兼容 chat/completions）

```bash
curl http://YOUR_HOST/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk-your-key' \
  -d '{
    "model": "gpt-image-2",
    "messages": [
      {
        "role": "user",
        "content": [
          {"type": "text", "text": "把这张图变成赛博朋克风格"},
          {"type": "image_url", "image_url": {"url": "https://example.com/photo.jpg"}}
        ]
      }
    ],
    "stream": true
  }'
```

### Python SDK

```python
from openai import OpenAI

client = OpenAI(base_url="http://YOUR_HOST/v1", api_key="sk-your-key")

# 文生图
resp = client.images.generate(
    model="gpt-image-2",
    prompt="a sunset over mountains",
    size="1024x1024",
)
print(resp.data[0].url)
```

## 账号导入

管理后台 → 账号池 → 导入，支持：

| 模式 | 说明 |
|------|------|
| **Session JSON** | 粘贴 `chatgpt.com/api/auth/session` 完整 JSON（推荐） |
| **AT** | 每行一个 Access Token |
| **RT** | 每行一个 Refresh Token（需填 client_id，建议绑代理） |
| **ST** | 每行一个 Session Token |

## 数据库表

| 表 | 用途 |
|----|------|
| `oai_accounts` | 上游账号（AT/RT/ST/状态/订阅类型） |
| `oai_account_cookies` | 账号 cookies（加密存储） |
| `account_proxy_bindings` | 账号-代理绑定 |
| `proxies` | 代理池 |
| `models` | 模型映射配置 |
| `image_tasks` | 图片任务（prompt/参考图/结果URL/耗时/重试次数） |
| `usage_logs` | 用量日志 |
| `system_settings` | 系统配置 KV |
| `admin_audit_logs` | 管理操作审计 |
| `backup_files` | 备份文件记录 |

## 管理后台页面

| 路径 | 功能 |
|------|------|
| `/personal/dashboard` | 运行概览（KPI + 账号/代理状态 + 每日趋势） |
| `/personal/play` | 在线体验（文生图 + 图生图） |
| `/personal/usage` | 用量观察 |
| `/personal/docs` | 接口文档 |
| `/admin/accounts` | 账号池管理 |
| `/admin/proxies` | 代理池管理 |
| `/admin/models` | 模型映射 |
| `/admin/usage` | 全局用量 |
| `/admin/audit` | 审计日志 |
| `/admin/backup` | 备份恢复 |
| `/admin/settings` | 系统设置（API Key、刷新策略、探测参数等） |

## 配置说明

主要配置 `configs/config.yaml`：

| 配置项 | 说明 |
|--------|------|
| `mysql.dsn` | MySQL 连接串 |
| `redis.addr` | Redis 地址 |
| `crypto.aes_key` | 64位 hex AES-256 密钥（加密令牌/cookies/代理密码） |
| `admin.username/password` | 管理后台登录凭据（默认 admin/admin123） |
| `upstream.base_url` | 上游地址（默认 `https://chatgpt.com`） |

Docker 部署通过 `deploy/.env` 覆盖配置。

## 目录结构

```
cmd/server/             服务入口
internal/
  account/              账号池（导入/刷新/调度）
  gateway/              OpenAI 兼容网关（chat/images/proxy）
  image/                图片任务（runner/DAO/model）
  scheduler/            轮询调度器
  proxy/                代理池
  model/                模型映射
  upstream/chatgpt/     ChatGPT 上游协议（SSE/文件上传/POW）
  middleware/           认证/CORS/日志/恢复
  settings/             系统设置
  usage/                用量统计
  audit/                审计日志
  backup/               备份恢复
web/                    Vue 3 + Element Plus 管理前端
sql/database.sql        初始化表结构
deploy/                 Docker Compose 部署
```
