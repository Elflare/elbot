// Agent 多个子包共同使用的对话契约与消息转换。
package share

import (
	"context"
	"elbot/internal/operationresult"

	"elbot/internal/contextmgr"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
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

// InputCommit stores canonical segments, not a protocol's decorated prompt.
func (m TurnMaterials) InputCommit(segments []llm.MessageSegment, rulesShown bool) storage.DialogueCommit {
	commit := storage.DialogueCommit{SessionID: m.Session.ID, Messages: m.Input.Reports, ElyphRulesShown: rulesShown}
	if user := m.UserMessage; user != nil {
		user.Content, user.Segments = MessageContent(segments), StoredMessageSegments(segments)
		commit.Messages = append(commit.Messages, user)
	}
	return commit
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
	PrepareInput(context.Context, context.Context, LoopInput, Output) (operationresult.Result[storage.DialogueCommit], error)
	RunLoop(Output) (operationresult.Result[LoopResult], error)
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

type PendingInput struct {
	Reports []*storage.Message // 已完成工具对，不占用户输入槽。
	// NeedsElyphRules is resolved after input hooks and before persistence.
	NeedsElyphRules bool
	Message         *storage.Message
	PlatformText    string
	Segments        []llm.MessageSegment
}

func DrainPending(turns interface {
	DrainMergedInput(string, string) turn.Input
}, sessionID string, expected string) *PendingInput {
	pending := turns.DrainMergedInput(sessionID, expected)
	if pending.Text == "" && len(pending.Segments) == 0 {
		if len(pending.Reports) > 0 {
			return &PendingInput{Reports: pending.Reports}
		}
		return nil
	}
	segments := append([]llm.MessageSegment(nil), pending.Segments...)
	if len(segments) == 0 {
		segments = llm.TextSegments(pending.Text)
	}
	message := storage.Message{
		SessionID: sessionID, Metadata: storage.EncodeInputIDs(pending.InputIDs),
		Role:     storage.RoleUser,
		Content:  MessageContent(segments),
		Segments: StoredMessageSegments(segments),
	}
	return &PendingInput{Reports: pending.Reports, Message: &message, PlatformText: pending.PlatformText, Segments: segments, NeedsElyphRules: pending.NeedsElyphRules}
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
	CommitDialogue(context.Context, storage.DialogueCommit) error
}

// NewToolBatch retains one assistant call array and ordered call/result links.
func NewToolBatch(ctx context.Context, allocate func(context.Context) (string, error), head *storage.Message) (*storage.ToolBatch, error) {
	if err := storage.PrepareMessageID(ctx, allocate, head); err != nil {
		return nil, err
	}
	head.CreatedAt = storage.Now()
	head.ToolBatchStatus = "pending"
	results := make([]*storage.Message, len(head.ToolCalls))
	ids := make([]string, len(head.ToolCalls))
	for i, call := range head.ToolCalls {
		row := ToolResultStorageMessage(head.SessionID, llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments("tool outcome is unknown: result has not been saved")})
		row.ToolResultStatus = "pending"
		results[i] = &row
		if err := storage.PrepareMessageID(ctx, allocate, &row); err != nil {
			return nil, err
		}
		ids[i] = row.ID
		row.CreatedAt = head.CreatedAt
	}
	fields, err := storage.DecodeSessionMetadata(head.Metadata)
	if err != nil {
		return nil, err
	}
	_ = fields.Set(storage.ToolResultMessageKey, ids)
	head.Metadata, _ = fields.Encode()
	return &storage.ToolBatch{Call: head, Results: results}, nil
}

// AppendToolTranscript projects committed slots in original call order, not completion order.
func AppendToolTranscript(dst *[]llm.LLMMessage, head *storage.Message, results []*storage.Message, remaining int) error {
	if remaining != 0 {
		return fmt.Errorf("tool batch has unfinished results")
	}
	for _, stored := range append([]*storage.Message{head}, results...) {
		message, err := MessageFromStorage(*stored)
		if err != nil {
			return err
		}
		*dst = append(*dst, message)
	}
	return nil
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
	ToolChoice                string // Empty inherits the configured choice; a tool-limit summary explicitly selects none.
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
