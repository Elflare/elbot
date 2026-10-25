// 准备 Elwisp 远程请求，在统一执行入口放行后发送并转换返回结果。
package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"elbot/internal/llm"
	toolshare "elbot/internal/tool/share"
)

type elwispToolRequest struct {
	Tool      string          `json:"tool"`
	EventKey  string          `json:"event_key,omitempty"`
	Arguments json.RawMessage `json:"arguments"`
}

type elwispToolResponse struct {
	Content string `json:"content"`
	Text    string `json:"text"`
	Result  string `json:"result"`
	Error   string `json:"error"`
}

// prepareELwispTool 固定远程地址、请求体和超时，不发送请求或保留准备阶段的 context。
func prepareELwispTool(call llm.ToolCallRequest, resolved resolvedTool) (*toolshare.PreparedCall, error) {
	discovered := resolved.Discovered
	timeout := time.Duration(discovered.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	arguments := json.RawMessage(strings.TrimSpace(call.Arguments))
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	payload, err := json.Marshal(elwispToolRequest{Tool: discovered.Name, EventKey: discovered.EventKey, Arguments: arguments})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, discovered.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return toolshare.NewPreparedCall(
		toolshare.CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)},
		toolshare.RiskAssessment{Level: toolshare.RiskLow},
		"",
		func(ctx context.Context) (*toolshare.Result, string, error) {
			return executeELwispTool(ctx, req, timeout)
		},
	), nil
}

// executeELwispTool 只发送已准备的请求，结果状态与开始边界由统一执行入口负责。
func executeELwispTool(ctx context.Context, req *http.Request, timeout time.Duration) (result *toolshare.Result, logContent string, callErr error) {
	defer func() {
		if callErr != nil {
			logContent = callErr.Error() + "\n" + logContent
		}
		logContent = toolshare.TruncateLogContent(logContent, 1024)
	}()
	toolCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.Clone(toolCtx))
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("ELwisp tool HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	content := strings.TrimSpace(string(body))
	var parsed elwispToolResponse
	if err := json.Unmarshal(body, &parsed); err == nil {
		content = firstELwispResult(parsed.Content, parsed.Text, parsed.Result, content)
		if strings.TrimSpace(parsed.Error) != "" {
			content = firstELwispResult(parsed.Content, parsed.Text, parsed.Result)
			if content != "" {
				result = &toolshare.Result{Content: content}
			}
			return result, content, fmt.Errorf("ELwisp tool error: %s", strings.TrimSpace(parsed.Error))
		}
	}
	return &toolshare.Result{Content: content}, content, nil
}

func firstELwispResult(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
