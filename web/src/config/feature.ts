// 前端 feature flag。以后要恢复文字模型的 UI,只需把 ENABLE_CHAT_MODEL 改回 true。
// 当前关闭原因:文字通路受 chatgpt.com 新 sentinel 协议影响,在 solver 接入前静默拒绝率较高。
// 后端路由仍保留,方便后续重新开启和调试。
export const ENABLE_CHAT_MODEL = false
