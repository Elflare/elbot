package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type assistantMetadata struct {
	ToolCalls []llm.ToolCallRequest `json:"tool_calls,omitempty"`
	RawText   string                `json:"raw_text,omitempty"`
}

func toolCallStorageMessage(sessionID, content, rawText string, calls []llm.ToolCallRequest) storage.Message {
	metadata := assistantMetadata{ToolCalls: calls}
	if rawText != "" && rawText != content {
		metadata.RawText = rawText
	}
	data, _ := json.Marshal(metadata)
	return storage.Message{SessionID: sessionID, Role: storage.RoleAssistant, Content: content, Metadata: string(data)}
}

func storedMessageSegments(segments []llm.MessageSegment) string {
	if len(segments) == 0 || segmentsTextOnly(segments) {
		return ""
	}
	data, _ := json.Marshal(segments)
	return string(data)
}

func segmentsTextOnly(segments []llm.MessageSegment) bool {
	for _, segment := range segments {
		if segment.Type != llm.SegmentText {
			return false
		}
	}
	return true
}

func assistantRawTextMetadata(content, rawText string) string {
	if rawText == "" || rawText == content {
		return ""
	}
	data, _ := json.Marshal(assistantMetadata{RawText: rawText})
	return string(data)
}

func toolResultStorageMessage(sessionID string, message llm.LLMMessage) storage.Message {
	return storage.Message{
		SessionID:  sessionID,
		Role:       storage.RoleTool,
		Content:    llm.SegmentsContentText(message.Segments),
		ToolCallID: message.ToolCallID,
		Segments:   storedMessageSegments(message.Segments),
		Metadata:   toolNameMetadata(message.Name),
	}
}

func toolNameMetadata(name string) string {
	if name == "" {
		return ""
	}
	data, _ := json.Marshal(map[string]string{"name": name})
	return string(data)
}

func persistedToolMessage(message llm.LLMMessage) llm.LLMMessage {
	content := llm.SegmentsContentText(message.Segments)
	if message.Name != "discover_tool" || content == "" {
		return message
	}
	var result tool.DiscoveryResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return message
	}
	names := make([]string, 0, len(result.Tools))
	for _, discovered := range result.Tools {
		if discovered.Info.Name != "" {
			names = append(names, discovered.Info.Name)
		}
	}
	message.Segments = llm.TextSegments(fmt.Sprintf("discover_tool found tools: %s", joinNames(names)))
	return message
}

func (a *Agent) rememberDiscoveryResult(ctx context.Context, session *storage.Session, result *tool.Result) {
	if result == nil {
		return
	}
	if len(result.Data) > 0 {
		var discovery tool.DiscoveryResult
		if err := json.Unmarshal(result.Data, &discovery); err == nil {
			a.rememberDiscoveredTools(ctx, session, &discovery)
		}
	}
	a.rememberActivatedTools(ctx, session, result.Metadata)
	a.persistShownRuleCardFormats(ctx, session, metadataToolNames(result.Metadata[tool.MetadataShownRuleCardFormats]))
}

func (a *Agent) rememberActivatedTools(ctx context.Context, session *storage.Session, metadata map[string]any) {
	if len(metadata) == 0 || session == nil || a.toolRuntime.registry == nil {
		return
	}
	names := metadataToolNames(metadata[tool.MetadataActivateTools])
	if len(names) == 0 {
		return
	}
	policy := a.securityPolicy
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	actor := a.actor(ctx)
	discovery := &tool.DiscoveryResult{}
	for _, name := range names {
		if t, ok := a.toolRuntime.registry.Get(name); ok {
			info := t.Info()
			risk := info.Risk
			if risk == "" {
				risk = tool.RiskHigh
			}
			if !tool.InfoAvailableInContext(ctx, info) || !policy.CanUseTool(actor, risk, info.OwnerScoped) {
				continue
			}
			schema := t.Schema()
			discovery.Tools = append(discovery.Tools, tool.DiscoveredTool{Info: tool.PublicInfo{Name: name, Description: info.Description, Source: string(info.Source), ForegroundOnly: info.ForegroundOnly}, Schema: &schema})
		}
	}
	a.rememberDiscoveredTools(ctx, session, discovery)
}

func metadataToolNames(value any) []string {
	switch names := value.(type) {
	case []string:
		return names
	case []any:
		out := make([]string, 0, len(names))
		for _, name := range names {
			if text, ok := name.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func (a *Agent) persistTurnMessage(ctx context.Context, message *storage.Message, operation string) error {
	if err := a.store.Messages().Append(ctx, message); err != nil {
		a.audit("persistence_error", "session_id", message.SessionID, "operation", operation, "error", err.Error())
		return err
	}
	return nil
}

func (a *Agent) persistTurnMessages(ctx context.Context, sessionID, operation string, messages []storage.Message) error {
	for i := range messages {
		messages[i].SessionID = sessionID
		if err := a.persistTurnMessage(ctx, &messages[i], operation); err != nil {
			return err
		}
	}
	return nil
}

func joinNames(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	out := names[0]
	for _, name := range names[1:] {
		out += ", " + name
	}
	return out
}
