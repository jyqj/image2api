# GPT2API Local 架构说明

本版本定位为单人自用 OpenAI 兼容中转,不是 SaaS 分发平台。核心链路是:

```text
客户端 / OpenAI SDK → /v1 → 本地请求上下文 → 模型映射 → 账号调度 → chatgpt.com → usage/image 日志
```

## 运行模块

| 模块 | 职责 |
| --- | --- |
| `internal/server` | HTTP 路由、控制台静态资源、基础中间件 |
| `internal/middleware` | request id、访问日志、本地控制台上下文 |
| `internal/account` | 上游账号导入、刷新、状态、图片剩余量探测 |
| `internal/proxy` | 代理维护、探测、账号绑定 |
| `internal/model` | 对外 model slug 与上游模型名的映射 |
| `internal/scheduler` | 从账号池中选择可用账号并做冷却/释放 |
| `internal/gateway` | OpenAI 兼容 `/v1` 对话和图片入口 |
| `internal/image` | 图片任务、结果落库、本地签名图片代理 |
| `internal/usage` | 请求日志与聚合统计 |
| `internal/settings` | 本地 KV 设置与公开站点信息 |
| `internal/backup` | 数据库备份、上传、恢复 |
| `internal/audit` | 控制台写操作审计 |

## 请求链路

1. 客户端请求 `/v1/chat/completions` 或 `/v1/images/generations`。
2. 网关读取模型映射,确认该 slug 已开放。
3. 调度器选择可用上游账号,必要时带上绑定代理。
4. chatgpt.com 客户端发起真实请求。
5. 网关把结果转换为 OpenAI 兼容格式返回。
6. `usage_logs` 记录请求类型、模型、账号、状态、token、图片数量、耗时与错误码。
7. 图片结果进入 `image_tasks`,图片 URL 通过 `/p/img/:task_id/:idx` 代理输出。

## 表结构范围

当前初始化 SQL 只包含运维和转发必须的数据:

- `oai_accounts`
- `oai_account_cookies`
- `account_proxy_bindings`
- `proxies`
- `models`
- `usage_logs`
- `image_tasks`
- `system_settings`
- `admin_audit_logs`
- `backup_files`

`usage_logs` 不携带外部分发身份字段,仅作为本地运行观察数据。

## 控制台

控制台页面分为两组:

- 本地中转:`本地总览`、`在线体验`、`用量记录`、`接口文档`
- 运维管理:`上游账号池`、`代理池`、`模型映射`、`全局用量`、`审计日志`、`备份恢复`、`系统设置`

## 配置热更新

`system_settings` 中的站点信息、网关调度参数、账号刷新参数等可通过 `/admin/settings` 编辑。服务启动时会回填缺省值,修改后可手动重载缓存。
