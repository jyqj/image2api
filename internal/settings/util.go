package settings

import (
	"sort"
	"strconv"
	"strings"
)

func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}

// sprintKeys 把更新过的 key 列表拼成审计 detail,避免把明文敏感值写进审计日志
// (比如未来可能加入密钥字段)。
func sprintKeys(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "updated=" + strings.Join(keys, ",")
}
