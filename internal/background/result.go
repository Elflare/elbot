package background

import (
	"encoding/json"
	"fmt"
	"strings"
)

func ParseJSONResult(text string) (JSONResult, error) {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return JSONResult{}, fmt.Errorf("json object not found")
	}
	var result JSONResult
	if err := json.Unmarshal([]byte(text[start:end+1]), &result); err != nil {
		return JSONResult{}, err
	}
	return result, nil
}

func DefaultJSONRetryPrompt() string {
	return "你返回的格式有误，请严格使用 JSON 格式。不能包含 Markdown 代码块，不能包含 JSON 外的任何文字。格式：{\"completed\":true,\"need_report\":false,\"report\":\"\",\"report_segments\":[]}。need_report 表示是否需要向目标平台汇报；成功、失败或阻塞都可以请求汇报。report_segments 可选，用于附带图片/文件路径。" + PathInstruction()
}
