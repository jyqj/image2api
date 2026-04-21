# Image2API 使用指南

## 简介

Image2API 提供 OpenAI 兼容的图片生成接口，支持通过 `gpt-image-2` 模型生成高质量图片。支持文生图、图生图（参考图编辑），兼容流式和非流式响应。

---

## 快速开始

### 接口信息

| 项目 | 值 |
|---|---|
| Base URL | `https://your-domain.com` |
| 模型名称 | `gpt-image-2` |
| 认证方式 | `Authorization: Bearer <你的API Key>` |

### 支持的接口

| 接口 | 用途 | 格式 |
|---|---|---|
| `POST /v1/images/generations` | 文生图 / 图生图 | JSON |
| `POST /v1/images/edits` | 图生图 / 编辑 | multipart/form-data |
| `POST /v1/chat/completions` | Chat 格式生图（自动识别）| JSON，支持流式 |

---

## 方式一：Images API

### 文生图

```bash
curl -X POST https://your-domain.com/v1/images/generations \
  -H "Authorization: Bearer sk-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "一只橘猫坐在窗台上看夕阳，水彩画风格"
  }'
```

### 图生图（reference_images）

通过 `reference_images` 字段传入参考图 URL 或 base64：

```bash
curl -X POST https://your-domain.com/v1/images/generations \
  -H "Authorization: Bearer sk-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "把这张图片转换成梵高星空风格",
    "reference_images": [
      "https://example.com/my-photo.jpg"
    ]
  }'
```

`reference_images` 支持的格式：
- 图片 URL：`https://example.com/image.png`
- base64 Data URL：`data:image/png;base64,iVBOR...`
- 最多 4 张参考图

### 图生图（multipart 上传）

```bash
curl -X POST https://your-domain.com/v1/images/edits \
  -H "Authorization: Bearer sk-your-key" \
  -F "model=gpt-image-2" \
  -F "prompt=在这张试卷上填写答案" \
  -F "image=@/path/to/photo.jpg"
```

### 响应示例

```json
{
  "created": 1776700000,
  "task_id": "img_a1b2c3d4e5f6...",
  "data": [
    {
      "url": "https://your-domain.com/p/img/img_a1b2c3d4e5f6.../0?exp=...&sig=..."
    }
  ]
}
```

### 请求参数

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `model` | string | 是 | 固定填 `gpt-image-2` |
| `prompt` | string | 是 | 图片描述（支持中英文，建议详细描述） |
| `reference_images` | string[] | 否 | 参考图 URL 或 base64（图生图时使用） |
| `n` | int | 否 | 默认 1 |
| `size` | string | 否 | 默认 `1024x1024`（实际尺寸由模型决定） |

---

## 方式二：Chat Completions API

### 纯文生图

```bash
curl -X POST https://your-domain.com/v1/chat/completions \
  -H "Authorization: Bearer sk-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "messages": [
      {"role": "user", "content": "画一幅赛博朋克风格的城市夜景"}
    ]
  }'
```

### 图生图（发送图片 + 文字）

这是 Cherry Studio 等客户端最常用的方式——直接在聊天中粘贴/上传图片并附加文字指令：

```bash
curl -X POST https://your-domain.com/v1/chat/completions \
  -H "Authorization: Bearer sk-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "messages": [
      {
        "role": "user",
        "content": [
          {"type": "text", "text": "把这张照片转换成油画风格"},
          {"type": "image_url", "image_url": {"url": "https://example.com/photo.jpg"}}
        ]
      }
    ]
  }'
```

`image_url` 支持：
- 图片 URL：`https://example.com/image.png`
- base64 Data URL：`data:image/jpeg;base64,/9j/4AAQ...`（Cherry Studio 粘贴/截图时自动使用此格式）

### 流式响应

设置 `"stream": true` 即可获得 SSE 流式响应：

```bash
curl -X POST https://your-domain.com/v1/chat/completions \
  -H "Authorization: Bearer sk-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "stream": true,
    "messages": [
      {"role": "user", "content": "画一只猫"}
    ]
  }'
```

> 注意：由于图片生成需要 30-60 秒，流式模式下会先等待生成完成，然后一次性返回包含图片 URL 的 chunk。

### 响应格式

**非流式：**
```json
{
  "choices": [{
    "message": {
      "role": "assistant",
      "content": "![generated](https://your-domain.com/p/img/img_xxx/0?...)"
    }
  }]
}
```

**流式（SSE）：**
```
data: {"choices":[{"delta":{"role":"assistant","content":"![generated](https://...)"}}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

---

## 客户端配置

### Cherry Studio

1. 设置 → 模型服务商 → 添加
2. API 地址：`https://your-domain.com`
3. API Key：你的密钥
4. 模型列表手动添加：`gpt-image-2`
5. 新建对话，选择 `gpt-image-2`，输入提示词即可
6. **图生图**：在对话中直接粘贴/上传图片，附加文字指令

### ChatBox

1. 设置 → AI 服务商 → OpenAI API Compatible
2. API Host：`https://your-domain.com`
3. API Key：你的密钥
4. 模型名称：`gpt-image-2`

### NextChat / ChatGPT-Next-Web

1. 设置 → 接口地址：`https://your-domain.com`
2. API Key：你的密钥
3. 自定义模型名：`gpt-image-2`

### Python / OpenAI SDK

```python
from openai import OpenAI

client = OpenAI(
    api_key="sk-your-key",
    base_url="https://your-domain.com/v1"
)

# 文生图
response = client.images.generate(
    model="gpt-image-2",
    prompt="一只柴犬穿着宇航服在月球上散步",
)
print(response.data[0].url)

# 图生图（通过 chat completions）
response = client.chat.completions.create(
    model="gpt-image-2",
    messages=[{
        "role": "user",
        "content": [
            {"type": "text", "text": "把这张图转成水彩画"},
            {"type": "image_url", "image_url": {"url": "https://example.com/photo.jpg"}}
        ]
    }]
)
print(response.choices[0].message.content)
```

### Node.js / OpenAI SDK

```javascript
import OpenAI from 'openai';

const client = new OpenAI({
  apiKey: 'sk-your-key',
  baseURL: 'https://your-domain.com/v1',
});

// 文生图
const response = await client.images.generate({
  model: 'gpt-image-2',
  prompt: '一幅日式浮世绘风格的海浪',
});
console.log(response.data[0].url);
```

---

## Prompt 技巧

- **越详细越好**：描述主体、风格、构图、光线、色调
- **支持中英文**：效果相当
- **图生图时**：明确描述要做什么修改，如"转换成油画风格"、"在试卷上填写答案"、"把背景换成海滩"

### 示例

| 场景 | Prompt |
|---|---|
| 产品图 | `白色背景上的一杯拿铁咖啡，俯拍视角，专业产品摄影，柔和自然光` |
| 插画 | `一个女孩在樱花树下读书，日系水彩插画风格，温暖色调` |
| 海报 | `赛博朋克风格城市夜景海报，霓虹灯光，雨中倒影，竖版构图` |
| 图标 | `扁平设计风格的云存储图标，蓝白配色，圆角矩形底板` |
| 写实 | `金毛犬在草地上奔跑，阳光明媚，浅景深，佳能85mm镜头效果` |

---

## 常见问题

### Q: 生成需要多久？

通常 30-60 秒。首次使用新账号可能稍长（需要完成账号初始化）。

### Q: 图片链接会过期吗？

图片代理链接 30 天内有效，且可反复访问。

### Q: 图生图怎么用？

三种方式：

1. **Chat 方式（推荐）**：`/v1/chat/completions`，在 message content 中发送 `image_url` + `text`
2. **JSON 方式**：`/v1/images/generations`，在 `reference_images` 字段传图片 URL 或 base64
3. **Multipart 方式**：`/v1/images/edits`，通过 form-data 上传图片文件

### Q: 为什么图生图没有参考到我的图片？

请确认：
- 图片 URL 是公网可访问的（服务器需要能下载到）
- base64 格式需要包含 `data:image/...;base64,` 前缀
- 文件大小不超过 20MB

### Q: 返回 503 / 无可用账号？

服务端账号池暂时繁忙，请稍后重试。

### Q: 返回 400 / content_policy_violation？

你的 prompt 触发了上游内容安全策略，请调整描述。错误信息中会包含具体原因。

### Q: 支持流式响应吗？

支持。在 `/v1/chat/completions` 中设置 `"stream": true`。图片生成完成后会通过 SSE 返回包含图片 URL 的 markdown。

### Q: 支持哪些图片尺寸？

尺寸由模型自动决定（通常为横版 1535x1024 或竖版 1024x1535），`size` 参数仅作记录用。
