// Agent 多个子包共同使用的对话契约与消息转换。
package share

import (
	"context"
	"elbot/internal/operationresult"

	"elbot/internal/contextmgr"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

type Outcome uint8

const (
	Completed Outcome = iota
	Paused
	Stopped
	Canceled
	Failed
	Superseded
)

type TurnInput struct {
	Session                  *storage.Session
	Text                     string
	Input                    turn.Input
	ReplyToPlatformMessageID string
	Selection                modelmgr.Selection
	RequestID                string
	Prepared                 PreparedTurn
}
type TurnMaterials struct {
	Session     *storage.Session
	Input       turn.Input
	UserMessage *storage.Message
	Loaded      *contextmgr.LoadedContext
}
type LoopInput struct {
	Session   *storage.Session
	Selection modelmgr.Selection
	RequestID string
	StartedAt time.Time
}

// Each prepared loop keeps its own typed protocol state for this turn.
type Loop interface {
	CheckSelection(modelmgr.Selection) operationresult.Result[struct{}]
	PrepareTurn(context.Context, TurnMaterials) (operationresult.Result[PreparedLoop], error)
}
type PreparedLoop interface {
	InputCommitter() MessageCommitter
	PrepareInput(context.Context, context.Context, LoopInput, Output) (operationresult.Result[*storage.Message], error)
	RunLoop(context.Context, context.Context, LoopInput, Output) (operationresult.Result[LoopResult], error)
}
type LoopResolver interface {
	LoopFor(llm.APIType) (Loop, error)
}

type LoopResult struct {
	Outcome   Outcome
	Commit    ReplyCommitInput
	Usage     *llm.Usage
	Selection modelmgr.Selection
}
type TurnResult struct {
	Outcome   Outcome
	Committed ReplyCommitResult
	Usage     *llm.Usage
	Selection modelmgr.Selection
	StartedAt time.Time
}

type Output interface {
	SendTo(context.Context, delivery.Target, []delivery.Output) (operationresult.Result[delivery.Receipt], error)
	StartStream(ctx context.Context) delivery.MessageStream
	Send(context.Context, delivery.StreamEvent) (delivery.Receipt, error)
	// Notify publishes a business fact to its captured conversation; callers
	// choose its lifetime. SendPreview remains tied to the active operation.
	Notify(ctx context.Context, level slog.Level, text string)
	// SendPreview takes one complete notice per tool call, including multiline
	// bodies; pass batch notices separately rather than joining them. General
	// workflow notices occupy one argument. Nonblank notices get one prefix each
	// and are joined before a single output Hook and notification.
	SendPreview(ctx context.Context, text ...string)
}

func BufferAssistantOutput(ctx context.Context) bool {
	msg, ok := MessageContextFrom(ctx)
	return ok && msg.BufferAssistantOutput
}

type PendingUserMessage struct {
	// NeedsElyphRules is resolved after input hooks and before persistence.
	NeedsElyphRules bool
	Message         storage.Message
	MessageIndex    int
	PlatformText    string
	Segments        []llm.MessageSegment
}

func DrainPending(turns interface {
	DrainMergedInput(string, string) turn.Input
}, sessionID string, expected string) *PendingUserMessage {
	pending := turns.DrainMergedInput(sessionID, expected)
	if pending.Text == "" && len(pending.Segments) == 0 {
		return nil
	}
	segments := append([]llm.MessageSegment(nil), pending.Segments...)
	if len(segments) == 0 {
		segments = llm.TextSegments(pending.Text)
	}
	message := storage.Message{
		SessionID: sessionID,
		Role:      storage.RoleUser,
		Content:   MessageContent(segments),
		Segments:  StoredMessageSegments(segments),
	}
	return &PendingUserMessage{Message: message, PlatformText: pending.PlatformText, Segments: segments, NeedsElyphRules: pending.NeedsElyphRules}
}

// ForegroundNotice is appended to the request tail as a synthetic user message;
// it is never inserted into the system prompt or attributed to a platform user.
func ForegroundNotice(row *storage.Session) (llm.LLMMessage, bool) {
	if row == nil || !session.WasPromoted(row) {
		return llm.LLMMessage{}, false
	}
	return llm.LLMMessage{
		Role:     llm.RoleUser,
		Segments: llm.TextSegments("[系统提示]\n此会话已由用户接管，当前是普通前台对话。保留原任务目标和历史，但后台无人值守、自动汇报及强制 JSON 输出要求已经解除；按当前用户要求正常回复，工具遵循前台权限和确认规则。"),
	}, true
}

type ReplyCommitInput struct {
	Persistence      MessageCommitter
	Session          *storage.Session
	Text             string
	RawText          string
	ReasoningContent string
	PlatformText     string
	Stream           delivery.MessageStream
	Outputs          []delivery.Output
}

// A failed commit can still have delivered or persisted a reply. Receipt covers
// the assistant send; deferred outputs retain their existing delivery boundary.
type ReplyCommitResult struct {
	MessageID string
	RawText   string
	Receipt   delivery.Receipt
}

type ToolNameProvider interface {
	ToolNames(ctx context.Context, mode string, session *storage.Session, scope session.Scope) (PromptToolNames, error)
}

type SystemPromptRequest struct {
	Mode    string
	Session *storage.Session
	Scope   session.Scope
}

type ToolDepsFactory interface {
	ForTurn(Output, string) tool.RunnerDeps
}

// MessageCommitter commits a prepared business message, or a cursor-only reply.
// Protocol-private implementations can atomically attach their native state.
type MessageCommitter interface {
	Commit(context.Context, *storage.Message, []storage.PlatformMessageMap) error
}

// NewToolBatch retains one assistant call array and ordered call/result links.
func NewToolBatch(ctx context.Context, allocate func(context.Context) (string, error), head *storage.Message, calls []llm.ToolCallRequest, results []*storage.Message) (*storage.ToolBatch, error) {
	if err := storage.PrepareMessageID(ctx, allocate, head); err != nil {
		return nil, err
	}
	head.CreatedAt = storage.Now()
	ids := make([]string, len(results))
	head.ToolCalls = make([]storage.ToolCall, len(calls))
	for i, call := range calls {
		head.ToolCalls[i] = storage.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments}
	}
	for i, result := range results {
		if err := storage.PrepareMessageID(ctx, allocate, result); err != nil {
			return nil, err
		}
		ids[i] = result.ID
		result.CreatedAt = head.CreatedAt
	}
	fields, err := storage.DecodeSessionMetadata(head.Metadata)
	if err != nil {
		return nil, err
	}
	_ = fields.Set(storage.ToolResultMessageKey, ids)
	head.Metadata, _ = fields.Encode()
	return &storage.ToolBatch{Call: head, Results: results}, nil
}

// Material is an archived public content fragment. Index associates it with its
// owner's ordered records; the owner alone interprets those records.
type Material struct {
	Index int `json:"index"`
	// MessageID identifies the committed source, when the fragment has one.
	MessageID string               `json:"message_id,omitempty"`
	Segments  []llm.MessageSegment `json:"segments"`
}

// MessageFromStorage is the sole projection of archived business messages.
func MessageFromStorage(row storage.Message) (llm.LLMMessage, error) {
	segments, err := MessageSegmentsFromStorage(row.Segments)
	if err != nil {
		return llm.LLMMessage{}, fmt.Errorf("message %s segments: %w", row.ID, err)
	}
	message := llm.LLMMessage{Role: llm.MessageRole(row.Role), ToolCallID: row.ToolCallID}
	text := row.Content
	switch row.Role {
	case storage.RoleAssistant:
		if row.SourceContent != "" {
			text = row.SourceContent
		}
		message.ToolCalls = make([]llm.ToolCallRequest, len(row.ToolCalls))
		for i, call := range row.ToolCalls {
			message.ToolCalls[i] = llm.ToolCallRequest{ID: call.ID, Name: call.Name, Arguments: call.Arguments}
		}
	case storage.RoleTool:
		message.Name = row.ToolName
	}
	if len(segments) == 0 {
		segments = llm.TextSegments(text)
	}
	message.Segments = segments
	return message, nil
}

// MaterialMediaIDs collects retention references without opening media entities.
func MaterialMediaIDs(materials []Material, arguments []string, retained []string) []string {
	seen := map[string]bool{}
	ids := []string{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, id := range retained {
		add(id)
	}
	for _, material := range materials {
		for _, segment := range material.Segments {
			add(segment.MediaID)
		}
	}
	for _, argument := range arguments {
		for _, id := range media.ArgumentMediaIDs(argument) {
			add(id)
		}
	}
	return ids
}

func EncodeMaterials(materials []Material) string {
	if materials == nil {
		materials = []Material{}
	}
	data, _ := json.Marshal(materials)
	return string(data)
}

func DecodeMaterials(raw string) ([]Material, error) {
	var materials []Material
	if err := json.Unmarshal([]byte(raw), &materials); err != nil {
		return nil, err
	}
	for _, material := range materials {
		for _, segment := range material.Segments {
			if (segment.Type == llm.SegmentImage || segment.Type == llm.SegmentFile) && (segment.MediaID == "" || segment.URL != "") {
				return nil, fmt.Errorf("archived media requires a stable reference without an expanded URL")
			}
		}
	}
	return materials, nil
}

func ToolCallStorageMessage(sessionID, content, rawText string, calls []llm.ToolCallRequest, reasoningContent string) storage.Message {
	storedCalls := make([]storage.ToolCall, len(calls))
	for i, call := range calls {
		storedCalls[i] = storage.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments}
	}
	message := storage.Message{SessionID: sessionID, Role: storage.RoleAssistant, Content: content, ReasoningContent: reasoningContent, ToolCalls: storedCalls}
	if rawText != content {
		message.SourceContent = rawText
	}
	return message
}

func StoredMessageSegments(segments []llm.MessageSegment) string {
	if len(segments) == 0 || (len(segments) == 1 && segments[0].Text != "" && segments[0] == (llm.MessageSegment{Type: llm.SegmentText, Text: segments[0].Text})) {
		return ""
	}
	segments = append([]llm.MessageSegment(nil), segments...)
	for i := range segments {
		if segments[i].MediaID != "" {
			segments[i].URL = ""
		}
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

func ToolResultStorageMessage(sessionID string, message llm.LLMMessage) storage.Message {
	return storage.Message{
		SessionID:  sessionID,
		Role:       storage.RoleTool,
		Content:    MessageContent(message.Segments),
		ToolCallID: message.ToolCallID,
		ToolName:   message.Name,
		Segments:   StoredMessageSegments(message.Segments),
	}
}

func MessageSegmentsFromStorage(raw string) ([]llm.MessageSegment, error) {
	if raw == "" {
		return nil, nil
	}
	var segments []llm.MessageSegment
	if err := json.Unmarshal([]byte(raw), &segments); err != nil {
		return nil, err
	}
	return segments, nil
}

// MessageContent preserves pure text verbatim. Multimodal segments remain the
// complete source; their content field is a searchable textual representation.
func MessageContent(segments []llm.MessageSegment) string {
	if !segmentsTextOnly(segments) {
		return llm.SegmentsContentText(segments)
	}
	var text strings.Builder
	for _, segment := range segments {
		text.WriteString(segment.Text)
	}
	return text.String()
}

type PreparedTurn interface {
	PrepareTurn(context.Context) (operationresult.Result[PreparedLoop], error)
}

type CallInput struct {
	Messages, RequestMessages []llm.LLMMessage
	Tools                     []llm.ToolSchema
}
type CallOutput struct {
	Text, SourceText string
	ReasoningContent string
	Usage            *llm.Usage
	ToolCalls        []llm.ToolCallRequest
	Outputs          []delivery.Output
}

// CallObservation exposes this call's mutable facts; publication stays in Dialogue.
type CallObservation interface {
	Event() *ModelCallCompletedEvent
	LogRecord() *events.LogRecord
	Publish(context.Context, error)
}

// ToolTranscriptCommitter retains ordered results for the current Chat batch.
type ToolTranscriptCommitter interface {
	tool.ToolCommitter
	Committed() []llm.LLMMessage
}
