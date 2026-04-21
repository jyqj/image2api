# ChatGPT 图像生成 & 额度查询接口备忘录

> 本文记录我们目前复用的 `chatgpt.com` 后端接口，及其请求/响应关键字段。
> 所有接口都走同一个 `Bearer {AUTH_TOKEN}`，host 固定 `https://chatgpt.com`。
> 运行环境：Go 版统一走 `internal/upstream/chatgpt` 的 uTLS transport + browser/Oai-* headers + cookie jar；探针和真实 `f/conversation` 共用同一套指纹体系。

---

## 0. 通用请求头

绝大多数接口共用下面这套头，区别只在于 `referer` / `x-openai-target-*`：

```
authorization: Bearer <AT>
accept: */*
accept-language: zh-CN,zh;q=0.9,en;q=0.8
content-type: application/json
origin: https://chatgpt.com
referer: https://chatgpt.com/                           # 或 /c/{conversation_id}
user-agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36
             (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36 Edg/143.0.0.0
oai-language: zh-CN
oai-device-id: <稳定 UUID>
oai-client-version: prod-2d84edefecf794f1bf3178f1f15e1005067d903d
oai-client-build-number: 5983180
```

> `AUTH_TOKEN` 是 Bearer JWT，有效期约 10 天。过期后需要重新从页面 network 中获取。

---

## 1. image 能力探测与 quota 诊断

### 1.1 `GET /backend-api/models` —— 主能力探针

用途：判断当前登录态是否具备 image 入口资格。
实测发现同一个 Pro / image 灰度号可能在 `/backend-api/models` 明确暴露 image 能力,但 `/backend-api/conversation/init` 仍返回 `blocked_features:image_gen`。因此 **models 是主探针,init 只是弱诊断**。

关键字段：

| 字段 | 含义 |
|---|---|
| `default_model_slug` | 当前默认上游模型,灰度号常见 `gpt-5-3` |
| `models[].enabledTools` | 包含 `image_gen_tool_enabled` 时说明模型具备 image 工具入口 |
| `models[].supportedFeatures` | 包含 `image` 时说明该模型支持图像能力 |
| `model_picker_version` | 前端模型选择器版本 |
| `fConversationEndpoint` | 前端是否走 `f/conversation` 族端点 |

gpt2api 的账号能力判断:

```text
enabledTools contains image_gen_tool_enabled
OR supportedFeatures contains image
=> image_capability_status = enabled
```

注意:这只表示“有机会进入 image / picture_v2 链路”,不表示本次请求一定命中 IMG2。

### 1.2 `POST /backend-api/conversation/init` —— quota / blocked_features 弱诊断

用途：保留 quota、reset 时间、banner、blocked_features 等诊断信息。
不要再用它作为 image2 是否可用的主判据。

请求体：

```json
{
  "gizmo_id": null,
  "requested_default_model": null,
  "conversation_id": null,
  "timezone_offset_min": -480,
  "system_hints": ["picture_v2"]
}
```

`timezone_offset_min` 只表示对齐前端抓包值,不要把它解释成服务器能力判据。

关键字段：

| 字段 | 含义 |
|---|---|
| `limits_progress[].feature_name == "image_gen"` | 生图 quota 诊断;部分灰度号这里可能缺失或异常 |
| `limits_progress[].remaining` | 当前剩余次数 |
| `limits_progress[].reset_after` | 下次重置时间 |
| `blocked_features` | 仅作风险/兼容诊断;可能与 `/models` 暴露能力矛盾 |
| `default_model_slug` | 旧探针视角下的默认模型,不等于 image 主链路实际模型 |

## 2. 生图完整调用链

按顺序编号：

```
[1] GET  /                                            → Bootstrap,让 cookie jar 收集一方 cookie
[2] GET  /backend-api/models                            → 主能力探针:models-first
[3] POST /backend-api/sentinel/chat-requirements        → 拿 chat_token (+可选 POW / Turnstile)
[4] POST /backend-api/f/conversation/prepare            → 拿 conduit_token（请求级抽卡关键）
[5] POST /backend-api/f/conversation    (SSE)           → 正式下发 prompt,流式拿 image refs
[6] GET  /backend-api/conversation/{conv_id}            → 轮询补齐 mapping/tool payload
[7] GET  多路 files/download fallback                   → 拿短期签名 URL 或直接图片 bytes
[8] GET  <signed_url>                                   → 下载图片 bytes
```

> `/conversation/init` 不在主生图链路内;只在账号探测/诊断任务里调用。复用现有会话时仍需每次执行 `[3][4][5]`,因为 sentinel 与 conduit_token 都是请求级信号。

---

### [2] `POST /backend-api/sentinel/chat-requirements`

作用：拿 `chat_token`（写进 `openai-sentinel-chat-requirements-token`），以及判断是否要做 POW / Turnstile。

请求体：

```json
{ "p": "gAAAAAC...<get_requirements_token 生成>" }
```

响应关键字段：

```json
{
  "token": "...chat_token...",
  "proofofwork": {
    "required": true,
    "seed": "...",
    "difficulty": "0fffff"
  }
}
```

如果 `proofofwork.required=true`，需用本地 SHA3-512 暴力算 `openai-sentinel-proof-token`（见 `gen_image.py` 的 `generate_proof_token`）。

---

### [4] `POST /backend-api/f/conversation/prepare`

作用：请求级/会话级分流。服务器在这里决定本次请求更可能走哪套生图后端,返回一个 `conduit_token`。这不是账号静态开关,同一账号不同请求也可能抽到不同结果。

请求头额外需要：

```
openai-sentinel-chat-requirements-token: <chat_token>
openai-sentinel-proof-token: <proof_token>     # 若 POW required
```

请求体：

```json
{
  "model": "auto",
  "system_hints": ["picture_v2"],
  "timezone_offset_min": -480,
  "conversation_id": null,              // 或已有会话 id
  "message_id": "<前端生成 UUID>",
  "supports_buffering": true
}
```

响应体：

```json
{ "conduit_token": "ct_...." }
```

`conduit_token` 要在 `[5]` 里通过请求头 `x-conduit-token` 传回去。

---

### [5] `POST /backend-api/f/conversation` (SSE)

作用：正式提交 prompt 并接收流式响应，里面会陆续下发 `image_gen_task_id` / 初始 `file_id`。

请求头额外需要：

```
openai-sentinel-chat-requirements-token: <chat_token>
openai-sentinel-proof-token: <proof_token>
x-conduit-token: <conduit_token>             # 关键！否则不进灰度桶
accept: text/event-stream
```

请求体骨架（精简）：

```json
{
  "action": "next",
  "messages": [{
      "id": "<msg_uuid>",
      "author": { "role": "user" },
      "content": { "content_type": "text", "parts": ["<prompt>"] },
      "metadata": { "system_hints": ["picture_v2"] }
  }],
  "parent_message_id": "<head_or_new_uuid>",
  "model": "auto",
  "conversation_id": null,
  "system_hints": ["picture_v2"],            // ← 必须，开启图像工具
  "timezone_offset_min": -480,
  "client_prepare_state": "sent",
  "supports_buffering": true,
  "enable_message_followups": true,
  "force_parallel_switch": "auto"
}
```

SSE 事件里要抓的字段：

| 字段 | 位置 | 作用 |
|---|---|---|
| `conversation_id` | `message.metadata` 或顶层 | 后续轮询用 |
| `image_gen_task_id` | `message.metadata.image_gen_async` | 确认任务已发起 |
| `content.parts[].asset_pointer` | assistant/tool 消息 | `file-service://file_xxx` 或 `sediment://file_xxx` |
| `content.parts[].metadata.generation.gen_size_v2` | image asset metadata | 新实测 IMG2 sediment-only 终稿关键指纹 |
| `image_gen_task_id` / `async_task_type=image_gen` | metadata | 确认异步图像任务已发起 |

---

### [6] `GET /backend-api/conversation/{conversation_id}`

作用：SSE 结束后轮询补齐最终 file-service URL（尤其灰度会出第二张高清图）。

响应：完整会话 JSON，结构里 `mapping` 是消息树。

polling 策略（见 `poll_conversation_for_images`）：
- 使用 **baseline diff**：请求前先记录 "现有 tool 消息 id 集合"，轮询时只看新增的。
- `file-service://` 直接视作 IMG2 终稿指纹。
- `sediment://` 不能一律视作 preview;若 asset metadata 含 `generation.gen_size_v2`,按 IMG2 sediment-only 终稿处理。
- 多条新增 image tool 消息仍可作为 IMG2 聚合信号。
- 单条 `sediment://` 且无 `gen_size_v2`,等待后仍无新终稿才判 `preview_only`。
- 最大等待由 runner 配置控制;连续 429 退避/中止。

---

### [7] 图片下载 URL fallback

当前上游下载端点不完全稳定,需要多路 fallback:

```text
/backend-api/files/download/{fid}?conversation_id={cid}&inline=false
/backend-api/conversation/{cid}/attachment/{sid}/download
/backend-api/files/download/{fid}
/backend-api/files/{fid}/download
```

响应可能是 JSON、302 Location,也可能直接返回图片二进制:

```json
{ "status": "success", "download_url": "https://files.oaiusercontent.com/…签名URL…" }
```

---

## 3. 其他已观察到的接口（非必用）

| 接口 | 方法 | 用途 | 响应 |
|---|---|---|---|
| `/backend-api/image-gen/image-paragen-display` | POST | **前端上报**：告诉后端"已展示 N 张图" | 204 空 |
| `/backend-api/conversation/{id}/async-status` | POST `{"status":null}` | 异步任务健康检查 | `{"status":"OK"}` |
| `/backend-api/accounts/check/v4-2023-04-27` | GET | 账号 features/entitlements | 旧诊断接口;不再作为 image 主判据 |
| `/backend-api/files/library` | POST | 用户图像库列表 | 不用于本流程 |
| `/backend-api/models` | GET | 当前账号可用模型 / image 工具入口 | **主能力探针** |
| `/backend-api/me` | GET | 用户基本信息 | 诊断用 |

---

## 4. 关键排查经验

1. **能力入口**：优先看 `/backend-api/models`。`enabledTools:image_gen_tool_enabled` 或 `supportedFeatures:image` 才是 image 入口资格的主信号。
2. **quota/blocked 诊断**：`/conversation/init` 只看 `limits_progress`、`reset_after`、`blocked_features` 作为辅助;不要用它否定 `/models` 的 image 能力。
3. **IMG2 命中**：本次是否命中看真实 `f/conversation` 结果。`file-service://` 或 `sediment:// + generation.gen_size_v2` 都应算 IMG2 协议命中。
4. **抽卡机制**：IMG2 是「账号资格 + 请求/会话抽卡」。同号可能本次 hit、下次 preview_only;调度器需要长期记录 `img2_hit_count / preview_only / miss / delivery_success`。
5. **风控**：HTTP 403、Turnstile、429、下载签名失败要分开看。协议命中不等于交付成功。
6. **TLS / 指纹**：探针和真实 runner 都应复用同一个上游 client: uTLS transport、Oai-* headers、稳定 device/session、cookie jar、代理绑定。

## 5. 相关脚本索引

| 脚本 | 用途 |
|---|---|
| `gen_image.py` | 主生图流程（含重试/轮询/下载） |
| `_check_image_gen_quota.py` | **仅查 `image_gen` 余额**，不消耗额度 |
| `_dump_acc.py` | 完整 dump `/accounts/check`，用于看 feature flag |
| `_check_quota.py` | 遍历多个诊断接口（me/models/accounts/check） |
| `_scan_har_gen.py` / `_scan_har_quota.py` | 扫 HAR 找接口/关键字段 |
| `_har_gen_endpoints.py` / `_dump_init.py` | Dump HAR 里特定接口的完整请求响应 |

---

_最后更新：2026-04-17_
