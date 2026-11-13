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
	"elbot/internal/operationresult"
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
func prepareELwispTool(call llm.ToolCallRequest, resolved resolvedTool) (operationresult.Result[*toolshare.PreparedCall], error) {
	discovered := resolved.Discovered
	timeout := time.Duration(discovered.TimeoutSeconds) * time.Second
	arguments := json.RawMessage(strings.TrimSpace(call.Arguments))
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if !json.Valid(arguments) {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: "ELwisp tool arguments must be valid JSON"}, nil
	}
	payload, err := json.Marshal(elwispToolRequest{Tool: discovered.Name, EventKey: discovered.EventKey, Arguments: arguments})
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{}, err
	}
	req, err := http.NewRequest(http.MethodPost, discovered.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(
		toolshare.CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)},
		toolshare.RiskAssessment{Level: discovered.Risk},
		nil,
		func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
			return executeELwispTool(ctx, req, timeout)
		},
	)}, nil
}

// executeELwispTool 只发送已准备的请求，结果状态与开始边界由统一执行入口负责。
func executeELwispTool(ctx context.Context, req *http.Request, timeout time.Duration) (operationresult.Result[*toolshare.Result], string, error) {
	toolCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.Clone(toolCtx))
	if err != nil {
		return operationresult.Result[*toolshare.Result]{}, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return operationresult.Result[*toolshare.Result]{}, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := fmt.Sprintf("ELwisp tool HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Failed, Message: message}, "", nil
	}
	content := strings.TrimSpace(string(body))
	var parsed elwispToolResponse
	if err := json.Unmarshal(body, &parsed); err == nil {
		if strings.TrimSpace(parsed.Error) != "" {
			message := fmt.Sprintf("ELwisp tool error: %s", strings.TrimSpace(parsed.Error))
			if content := firstELwispResult(parsed.Content, parsed.Text, parsed.Result); content != "" {
				message += "\n" + content
			}
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Failed, Message: message}, "", nil
		}
		content = firstELwispResult(parsed.Content, parsed.Text, parsed.Result, content)
	}
	return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Content: content}}, "", nil
}

func firstELwispResult(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
