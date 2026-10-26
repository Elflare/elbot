// Agent 多个子包共同使用的对话契约与消息转换。
package share

import (
	"context"

	"elbot/internal/contextmgr"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/platform"
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
	Text      string
	Selection modelmgr.Selection
	RequestID string
	StartedAt time.Time
}

// Each prepared loop keeps its own typed protocol state for this turn.
type Loop interface {
	CheckSelection(modelmgr.Selection) error
	PrepareTurn(context.Context, TurnMaterials) (PreparedLoop, error)
}
type PreparedLoop interface {
	InputCommitter() MessageCommitter
	PrepareInput(context.Context, context.Context, LoopInput, Output) (*storage.Message, error)
	RunLoop(context.Context, context.Context, LoopInput, Output) LoopResult
}
type LoopResolver interface {
	LoopFor(llm.APIType) (Loop, error)
}

type LoopResult struct {
	Outcome           Outcome
	Err               error
	QuietCancellation bool
	Commit            ReplyCommitInput
	Usage             *llm.Usage
	Selection         modelmgr.Selection
}
type TurnResult struct {
	Outcome           Outcome
	Err               error
	QuietCancellation bool
	Committed         ReplyCommitResult
	Usage             *llm.Usage
	Selection         modelmgr.Selection
	StartedAt         time.Time
}

type Output interface {
	StartReasoning(context.Context) delivery.ReasoningStream
	StartStream(ctx context.Context) (delivery.MessageStream, error)
	FinishIntermediate(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string, streaming bool) error
	ReplaceAndFinishStream(ctx context.Context, streamCtx context.Context, stream delivery.MessageStream, text string) (delivery.Receipt, events.LogResult, error)
	SendAssistant(ctx context.Context, text string) (delivery.Receipt, events.LogResult, error)
	// SendOutputs returns the source log result and original business error;
	// partial delivery facts belong to the source log body, not a returned receipt.
	SendOutputs(ctx context.Context, outputs []delivery.Output) (events.LogResult, error)
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
	msg, ok := platform.MessageContextFrom(ctx)
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
	DrainMergedInput(string, ...string) turn.Input
}, sessionID string, expected ...string) *PendingUserMessage {
	pending := turns.DrainMergedInput(sessionID, expected...)
	if pending.Text == "" && len(pending.Segments) == 0 {
		return nil
	}
	segments := append([]llm.MessageSegment(nil), pending.Segments...)
	if len(segments) == 0 {
		segments = llm.TextSegments(pending.Text)
	}
	message := storage.Message{
		ID:        storage.NewID(),
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
	Persistence  MessageCommitter
	Session      *storage.Session
	Text         string
	RawText      string
	PlatformText string
	Stream       delivery.MessageStream
	Outputs      []delivery.Output
}

// A failed commit can still have delivered or persisted a reply. Receipt covers
// the assistant send; deferred outputs retain their existing delivery boundary.
type ReplyCommitResult struct {
	MessageID  string
	RawText    string
	Receipt    delivery.Receipt
	Persisted  bool
	SendErr    error
	PersistErr error
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
	Commit(context.Context, *storage.Message, []storage.PlatformMessageMap) (events.LogResult, error)
}

// NewToolBatch retains one assistant call array and ordered call/result links.
func NewToolBatch(head *storage.Message, calls []llm.ToolCallRequest, results []*storage.Message) (*storage.ToolBatch, error) {
	if head.ID == "" {
		head.ID = storage.NewID()
	}
	head.CreatedAt = storage.Now()
	ids := make([]string, len(results))
	for i, result := range results {
		if result.ID == "" {
			result.ID = storage.NewID()
		}
		ids[i] = result.ID
		result.CreatedAt = head.CreatedAt
	}
	fields, err := storage.DecodeSessionMetadata(head.Metadata)
	if err != nil {
		return nil, err
	}
	// Calls and IDs contain only strings; decoded metadata already contains valid JSON.
	_ = fields.Set("tool_calls", calls)
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
		metadata, err := AssistantMessageMetadata(row.Metadata)
		if err != nil {
			return llm.LLMMessage{}, fmt.Errorf("message %s metadata: %w", row.ID, err)
		}
		if metadata.RawText != "" {
			text = metadata.RawText
		}
		message.ToolCalls = metadata.ToolCalls
	case storage.RoleTool:
		message.Name, err = ToolNameFromMetadata(row.Metadata)
		if err != nil {
			return llm.LLMMessage{}, fmt.Errorf("message %s metadata: %w", row.ID, err)
		}
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

func EncodeMaterials(materials []Material) (string, error) {
	if materials == nil {
		materials = []Material{}
	}
	data, err := json.Marshal(materials)
	return string(data), err
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

type AssistantMetadata struct {
	ToolCalls []llm.ToolCallRequest `json:"tool_calls,omitempty"`
	RawText   string                `json:"raw_text,omitempty"`
}

func ToolCallStorageMessage(sessionID, content, rawText string, calls []llm.ToolCallRequest) storage.Message {
	metadata := AssistantMetadata{ToolCalls: calls}
	if rawText != "" && rawText != content {
		metadata.RawText = rawText
	}
	data, _ := json.Marshal(metadata)
	return storage.Message{SessionID: sessionID, Role: storage.RoleAssistant, Content: content, Metadata: string(data)}
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
		Segments:   StoredMessageSegments(message.Segments),
		Metadata:   ToolNameMetadata(message.Name),
	}
}

func ToolNameMetadata(name string) string {
	if name == "" {
		return ""
	}
	data, _ := json.Marshal(map[string]string{"name": name})
	return string(data)
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

func AssistantMessageMetadata(raw string) (AssistantMetadata, error) {
	var value AssistantMetadata
	if raw == "" {
		return value, nil
	}
	err := json.Unmarshal([]byte(raw), &value)
	return value, err
}

func ToolNameFromMetadata(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	var value struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", err
	}
	return value.Name, nil
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
	PrepareTurn(context.Context) (PreparedLoop, error)
}

type CallInput struct {
	Messages, RequestMessages []llm.LLMMessage
	Tools                     []llm.ToolSchema
}
type CallOutput struct {
	Text, SourceText string
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
