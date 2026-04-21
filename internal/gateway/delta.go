package gateway

import (
	"encoding/json"
	"strings"
)

// deltaExtractor 从 chatgpt.com 上游 SSE data 里提取可展示的增量文本。
//
// 逻辑对齐 chatgpt.com 浏览器抓包的 SSE 数据格式,
// 主要点:
//
//  1. 维护 `curP`(当前 JSON-Patch path)。一帧的 `p` 缺省时继承上一帧的 p
//     —— JSON Merge Patch 约定,chatgpt 的 v1 buffering 就按这个省流量。
//
//  2. 维护 `recipient`(当前 assistant message 的 recipient):
//     只有 "all"(真实回答)才当正文;其他如 "python" / "async_browser" /
//     "image_gen.text2im" 是 tool 调用,不输出。
//     recipient 在首帧 `{"v": {"message": {...}}}` 里出现。
//
//  3. 区分 thoughts:
//     curP 落在 `/message/content/thoughts/...` 的增量全部当"思考过程"
//     —— 对 OpenAI chat 语义,不回给客户端(未来想支持 reasoning
//     字段时再放进来)。
//
//  4. 正文候选只认 `/message/content/parts/0`(含 "" 空 p 继承到这个的情形)。
//     `v` 可能是:
//     a) string           —— 单帧增量
//     b) object 首帧       —— v = {"message":{...},"conversation_id":...}
//     c) array of patches —— v = [{p,o,v}, {p,o,v}, ...]
//
//  5. 结束判据:
//     - 顶层 `{"type":"message_stream_complete"}`
//     - 任何层级 patch 出现 `p == "/message/status"` 且 `v == "finished_successfully"`
//     - 老 /conversation 的 parts 全量 + status: finished_successfully(场景 1 兼容)
//     - 特殊事件 [DONE]
//
// 行为不变的保证:场景 1 仍兼容,`场景 2(patch 模式)` 的增量更稳。
type deltaExtractor struct {
	// lastFull 场景 1(旧协议)里用到,记录上一次全量正文,用于做差分。
	lastFull string

	// curP 保留上一帧的 patch path(v1 buffering 省略 p 时继承)。
	curP string

	// recipient 记录当前助手消息的接收方(all / python / ...).
	// 默认 "all":首帧没提供 recipient 字段时按 "all" 处理。
	recipient string
}

// Done 判断 data 是否是 [DONE]。
func isDone(data []byte) bool {
	s := strings.TrimSpace(string(data))
	return s == "[DONE]"
}

// Extract 返回:增量文本、是否 final(收到结束),err。
func (d *deltaExtractor) Extract(data []byte) (string, bool, error) {
	if d.recipient == "" {
		d.recipient = "all"
	}
	if isDone(data) {
		return "", true, nil
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		// 非 JSON(心跳或其他)—— 忽略。
		return "", false, nil
	}

	// 顶层 type: "message_stream_complete" —— f/conversation 结束事件。
	if t, _ := raw["type"].(string); t == "message_stream_complete" {
		return "", true, nil
	}

	// 1) 继承/更新 curP。只有本帧显式出现 p 时才覆盖,否则继承上一帧。
	if p, ok := raw["p"].(string); ok {
		d.curP = p
	}

	// 2) 遇到 thoughts 路径:v1 协议里 summary/thoughts 的 token 增量
	//    都不当作 assistant 正文(未来接 reasoning 字段时再放开)。
	if strings.HasPrefix(d.curP, "/message/content/thoughts") {
		return "", false, nil
	}

	v, hasV := raw["v"]
	if !hasV {
		return "", false, nil
	}

	// 3) v 为 string:最常见的增量帧。
	if s, ok := v.(string); ok {
		// 只有 recipient == "all"(真实回答)且 curP 落在 parts/0(或空继承)才当正文。
		if d.recipient != "all" {
			// 状态检查:一些上游用 `{"v":"finished_successfully","p":"/message/status"}` 作为结束
			if d.curP == "/message/status" && s == "finished_successfully" {
				return "", true, nil
			}
			return "", false, nil
		}
		if d.curP == "/message/status" && s == "finished_successfully" {
			return "", true, nil
		}
		// curP 为空(首 append 未声明 p)或显式 parts/0:输出
		if d.curP == "" || d.curP == "/message/content/parts/0" {
			return s, false, nil
		}
		return "", false, nil
	}

	// 4) v 为数组:一帧打包多个 patch。
	if arr, ok := v.([]interface{}); ok {
		var b strings.Builder
		final := false
		for _, item := range arr {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			// 每条 patch 也可能更新 curP;对数组内 patch,按每条单独解析,
			// 但为了跨帧继承,遍历完后再同步 curP(取最后一条为准)。
			subP, _ := m["p"].(string)
			subV := m["v"]
			subO, _ := m["o"].(string)
			if subP != "" {
				d.curP = subP
			}
			// thoughts 相关的 patch 吞掉
			if strings.HasPrefix(d.curP, "/message/content/thoughts") {
				continue
			}
			// 状态结束
			if d.curP == "/message/status" {
				if s, ok := subV.(string); ok && s == "finished_successfully" {
					final = true
				}
				continue
			}
			// 只认正文 append
			if (d.curP == "" || d.curP == "/message/content/parts/0") &&
				(subO == "" || subO == "append") && d.recipient == "all" {
				if s, ok := subV.(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String(), final, nil
	}

	// 5) v 为 object:通常是首帧,里面包着 conversation_id + message 元信息。
	if m, ok := v.(map[string]interface{}); ok {
		// 更新 recipient:首帧没带 role=assistant 时也可能是 tool,需要及时切
		if msg, ok := m["message"].(map[string]interface{}); ok {
			if r, ok := msg["recipient"].(string); ok && r != "" {
				d.recipient = r
			}
			// 初始 content(通常空),用来给老协议场景 1 设置 lastFull baseline
			if content, ok := msg["content"].(map[string]interface{}); ok {
				if parts, ok := content["parts"].([]interface{}); ok && len(parts) > 0 {
					if cur, ok := parts[0].(string); ok {
						d.lastFull = cur
						final := false
						if st, _ := msg["status"].(string); st == "finished_successfully" {
							final = true
						}
						// 首帧的初始内容也 yield 出去(通常是空,不会影响),
						// 保持向后兼容老代码读 parts[0] 的行为。
						if cur != "" && d.recipient == "all" {
							return cur, final, nil
						}
						return "", final, nil
					}
				}
			}
		}
		return "", false, nil
	}

	// 6) 场景 1 兼容:旧 /conversation 协议的顶层 {"message":{...}}
	if msg, ok := raw["message"].(map[string]interface{}); ok {
		if r, ok := msg["recipient"].(string); ok && r != "" {
			d.recipient = r
		}
		if content, ok := msg["content"].(map[string]interface{}); ok {
			if parts, ok := content["parts"].([]interface{}); ok && len(parts) > 0 {
				if cur, ok := parts[0].(string); ok {
					delta := ""
					if strings.HasPrefix(cur, d.lastFull) {
						delta = cur[len(d.lastFull):]
					} else {
						delta = cur
					}
					d.lastFull = cur
					final := false
					if status, _ := msg["status"].(string); status == "finished_successfully" {
						final = true
					}
					if d.recipient != "all" {
						return "", final, nil
					}
					return delta, final, nil
				}
			}
		}
	}

	return "", false, nil
}
