package delivery

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
)

type Kind string

const (
	KindText     Kind = "text"
	KindEmoticon Kind = "emoticon"
	KindImage    Kind = "image"
	KindFile     Kind = "file"
	KindRecord   Kind = "record"
	KindAt       Kind = "at"
	KindReply    Kind = "reply"
)

type Target struct {
	Platform      string `json:"platform,omitempty"`
	ScopeID       string `json:"scope_id,omitempty"`
	PrivateUserID string `json:"private_user_id,omitempty"`
	GroupID       string `json:"group_id,omitempty"`
	Superadmins   bool   `json:"superadmins,omitempty"`
}

const (
	DeliveryImmediate      = "immediate"
	DeliveryAfterAssistant = "after_assistant"
)

type temporaryConnectionKey struct{}

func WithTemporaryConnection(ctx context.Context) context.Context {
	return context.WithValue(ctx, temporaryConnectionKey{}, true)
}

func UseTemporaryConnection(ctx context.Context) bool {
	value, _ := ctx.Value(temporaryConnectionKey{}).(bool)
	return value
}

func (t Target) Empty() bool {
	return strings.TrimSpace(t.Platform) == "" && strings.TrimSpace(t.ScopeID) == "" && strings.TrimSpace(t.PrivateUserID) == "" && strings.TrimSpace(t.GroupID) == "" && !t.Superadmins
}

type Source struct {
	URL      string
	Path     string
	MediaID  string `json:"media,omitempty"`
	MIMEType string
	Data     []byte
}

// IsHTTPMediaSource reports whether value is an HTTP(S) media source.
func IsHTTPMediaSource(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}

// DeliveryInfo 保存输出的发送时机与 Hook 来源，随 Output 按值复制。
type DeliveryInfo struct {
	Timing    string // 发送时机：immediate 或 after_assistant；空值表示立即发送。
	HookPoint string // 产生输出的 Hook 点；非 Hook 输出为空。
	HookName  string // 产生输出的 Hook 名称，用于日志和错误归属。
	HookMode  string // Hook 执行模式：run 或 notify；非 Hook 输出为空。
}

// Action 是随消息投递的命令操作；平台只负责展示，执行仍经过命令入口。
type Action struct {
	ID      string // 操作标识，不作为授权凭据。
	Label   string // 用户看到的按钮文字。
	Command string // 点击后提交的完整命令，由业务层提供。
}

type Output struct {
	Kind                     Kind
	Text                     string
	Name                     string
	EmoticonID               string
	ReplyToPlatformMessageID string
	Source                   Source
	Target                   Target
	Delivery                 DeliveryInfo // 发送时机与产生该输出的 Hook 来源。
	Actions                  []Action     // 与本段正文绑定的操作；不支持按钮的平台保留正文。
}

func WithDeliveryTiming(out Output, timing string) Output {
	out.Delivery.Timing = strings.TrimSpace(timing)
	return out
}

func DeliveryTiming(out Output) string {
	timing := strings.TrimSpace(out.Delivery.Timing)
	if timing == "" {
		return DeliveryImmediate
	}
	return timing
}

func ValidateDeliveryTiming(timing string) error {
	switch strings.TrimSpace(timing) {
	case "", DeliveryImmediate, DeliveryAfterAssistant:
		return nil
	default:
		return fmt.Errorf("unsupported timing %q", timing)
	}
}

func SplitByDeliveryTiming(outputs []Output) ([]Output, []Output) {
	if len(outputs) == 0 {
		return nil, nil
	}
	immediate := make([]Output, 0, len(outputs))
	deferred := make([]Output, 0)
	for _, out := range outputs {
		if DeliveryTiming(out) == DeliveryAfterAssistant {
			deferred = append(deferred, out)
			continue
		}
		immediate = append(immediate, out)
	}
	return immediate, deferred
}

func Text(text string) Output {
	return Output{Kind: KindText, Text: text}
}

func Emoticon(id, name string) Output {
	return Output{Kind: KindEmoticon, EmoticonID: strings.TrimSpace(id), Name: strings.TrimSpace(name)}
}

func ImagePath(path string) Output {
	return Output{Kind: KindImage, Source: Source{Path: path}}
}

func FilePath(path string) Output {
	return Output{Kind: KindFile, Source: Source{Path: path}}
}

func RecordPath(path string) Output {
	return Output{Kind: KindRecord, Source: Source{Path: path}}
}

func At(userID string) Output {
	return Output{Kind: KindAt, Name: strings.TrimSpace(userID)}
}

func Reply(platformMessageID, text string) Output {
	return Output{Kind: KindReply, Text: text, ReplyToPlatformMessageID: strings.TrimSpace(platformMessageID)}
}

// SentMessage describes a platform message produced by a send operation.
type SentMessage struct {
	PlatformMessageID string
	Platform          string
	ScopeID           string
	OutputIndexes     []int
}

type Receipt struct {
	PlatformMessageIDs []string
	SentMessages       []SentMessage
}

// StreamingMessageSender is an optional platform capability for editable streaming delivery.
// Platforms can implement it with terminal replacement, message editing, or any equivalent mechanism.
type StreamingMessageSender interface {
	StartStream(ctx context.Context) (MessageStream, error)
}

// MessageStream represents one assistant message that can be appended while streaming
// and replaced with the final post-hook content.
// Replace and Finish return business receipts and independent source log results;
// neither result replaces the original business error or controls retry policy.
type MessageStream interface {
	Append(ctx context.Context, text string) error
	Replace(ctx context.Context, text string) (Receipt, globalevents.LogResult, error)
	Finish(ctx context.Context) (Receipt, globalevents.LogResult, error)
}

// ReasoningStream consumes raw model deltas in order for one call. Presentation
// failures are isolated from the business result. Finish closes any opened view,
// including on error or cancellation; implementations must tolerate repetition.
type ReasoningStream interface {
	Append(context.Context, string)
	Finish(context.Context)
}

// MessageSender sends one logical message, represented by ordered output segments.
// Receipt preserves actual delivery facts for business consumers. LogResult
// declares the operation's logging outcome and body before any caller wraps error.
type MessageSender interface {
	SendChat(ctx context.Context, outputs []Output) (Receipt, globalevents.LogResult, error)
	SendTo(ctx context.Context, target Target, outputs []Output) (Receipt, globalevents.LogResult, error)
}

type Manager struct {
	Sender MessageSender
}

func NewManager(sender MessageSender) Manager {
	return Manager{Sender: sender}
}

func (m Manager) SendChat(ctx context.Context, outputs []Output) (Receipt, globalevents.LogResult, error) {
	if err := ctx.Err(); err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if err == context.Canceled {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return Receipt{}, logResult, err
	}
	if _, err := ValidateOutputs(outputs); err != nil {
		return Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	return m.Sender.SendChat(ctx, outputs)
}

func (m Manager) SendTo(ctx context.Context, target Target, outputs []Output) (Receipt, globalevents.LogResult, error) {
	if err := ctx.Err(); err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if err == context.Canceled {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return Receipt{}, logResult, err
	}
	if len(outputs) == 0 {
		return Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped}, nil
	}
	outputTarget, err := ValidateOutputs(outputs)
	if err != nil {
		return Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	if target.Empty() {
		target = outputTarget
	}
	receipt, logResult, err := m.Sender.SendTo(ctx, target, outputs)
	LogSendResult(ctx, target, outputs[0], logResult)
	return receipt, logResult, wrapOutputSourceError(outputs[0], err)
}

// LogSendResult publishes the sending source's result. Native notification
// senders use this boundary when their protocol cannot use ordinary chat output.
func LogSendResult(ctx context.Context, target Target, out Output, logResult globalevents.LogResult) {
	if logResult.ResultStatus != globalevents.ResultFailed && logResult.ResultStatus != globalevents.ResultCanceled {
		return
	}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	record := globalevents.LogRecord{
		Category: globalevents.LogRuntime, Level: slog.LevelError,
		EventName: "chat_output_failed", Module: "delivery", ResultStatus: logResult.ResultStatus,
		Content: fmt.Sprintf("chat output failed: platform=%s hook=%q hook_point=%q hook_mode=%q\n%s", target.Platform, strings.TrimSpace(out.Delivery.HookName), strings.TrimSpace(out.Delivery.HookPoint), strings.TrimSpace(out.Delivery.HookMode), logResult.Content),
	}
	if logResult.ResultStatus == globalevents.ResultCanceled {
		record.Level = slog.LevelInfo
	}
	if facts.SessionID != "" {
		record.Identity.SessionID = &facts.SessionID
	}
	if facts.RunID != "" {
		record.Identity.RunID = &facts.RunID
	}
	if facts.Attempt != "" {
		record.Identity.Attempt = &facts.Attempt
	}
	if facts.RequestID != "" {
		record.Identity.RequestID = &facts.RequestID
	}
	if facts.RootRequestID != "" {
		record.Identity.RootRequestID = &facts.RootRequestID
	}
	_ = globalevents.EmitLog(ctx, record)
}

func wrapOutputSourceError(out Output, err error) error {
	if err == nil {
		return nil
	}
	if hookName := strings.TrimSpace(firstNonEmpty(out.Delivery.HookName, out.Delivery.HookPoint)); hookName != "" {
		return fmt.Errorf("hook %s output: %w", hookName, err)
	}
	return err
}

func FallbackOutput(outputs []Output) Output {
	parts := make([]string, 0, len(outputs))
	for _, out := range outputs {
		if text := strings.TrimSpace(FallbackText(out)); text != "" {
			parts = append(parts, text)
		}
	}
	return Text(strings.Join(parts, "\n"))
}

func ValidateOutputsTarget(outputs []Output) (Target, error) {
	var target Target
	for _, out := range outputs {
		if out.Target.Empty() {
			continue
		}
		if target.Empty() {
			target = out.Target
			continue
		}
		if target != out.Target {
			return Target{}, fmt.Errorf("outputs in one batch must use the same target")
		}
	}
	return target, nil
}

func ValidateOutputs(outputs []Output) (Target, error) {
	for i, out := range outputs {
		if err := ValidateDeliveryTiming(DeliveryTiming(out)); err != nil {
			return Target{}, fmt.Errorf("outputs[%d]: %w", i, err)
		}
		sourceCount := 0
		if strings.TrimSpace(out.Source.Path) != "" {
			sourceCount++
		}
		if strings.TrimSpace(out.Source.MediaID) != "" {
			sourceCount++
		}
		if strings.TrimSpace(out.Source.URL) != "" {
			sourceCount++
		}
		if len(out.Source.Data) > 0 {
			sourceCount++
		}
		switch out.Kind {
		case KindText:
			if sourceCount != 0 {
				return Target{}, fmt.Errorf("outputs[%d]: text output cannot have a media source", i)
			}
		case KindImage, KindFile, KindRecord:
			if sourceCount != 1 {
				return Target{}, fmt.Errorf("outputs[%d]: image/file/record output must have exactly one media source", i)
			}
			if path := strings.TrimSpace(out.Source.Path); strings.Contains(path, "://") {
				return Target{}, fmt.Errorf("outputs[%d]: media path must be a filesystem path, not a URI", i)
			}
			if value := strings.TrimSpace(out.Source.URL); value != "" {
				u, err := url.Parse(value)
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					return Target{}, fmt.Errorf("outputs[%d]: media URL must be an absolute HTTP(S) URL", i)
				}
			}
		case KindEmoticon:
			if sourceCount != 0 {
				return Target{}, fmt.Errorf("outputs[%d]: emoticon output cannot have a media source", i)
			}
			if strings.TrimSpace(out.EmoticonID) == "" {
				return Target{}, fmt.Errorf("outputs[%d]: emoticon output requires an emoticon ID", i)
			}
			if strings.TrimSpace(out.Name) == "" {
				return Target{}, fmt.Errorf("outputs[%d]: emoticon output requires a name", i)
			}
		case KindAt:
			if strings.TrimSpace(out.Name) == "" {
				return Target{}, fmt.Errorf("outputs[%d]: at output requires a user ID", i)
			}
		case KindReply:
			if strings.TrimSpace(out.ReplyToPlatformMessageID) == "" {
				return Target{}, fmt.Errorf("outputs[%d]: reply output requires a message ID", i)
			}
		default:
			return Target{}, fmt.Errorf("outputs[%d]: unsupported output kind %q", i, out.Kind)
		}
	}
	return ValidateOutputsTarget(outputs)
}

func FallbackText(out Output) string {
	var label string
	switch out.Kind {
	case KindText:
		return out.Text
	case KindEmoticon:
		return fmt.Sprintf("[表情: %s]", strings.TrimSpace(out.Name))
	case KindAt:
		name := strings.TrimSpace(firstNonEmpty(out.Name, out.Text))
		if name == "" {
			return ""
		}
		return fmt.Sprintf("@%s", name)
	case KindReply:
		replyID := strings.TrimSpace(out.ReplyToPlatformMessageID)
		if replyID == "" {
			return out.Text
		}
		return fmt.Sprintf("[引用消息 %s]\n%s", replyID, out.Text)
	case KindImage:
		label = "图片"
	case KindFile:
		label = "文件"
	case KindRecord:
		label = "语音"
	default:
		return firstNonEmpty(out.Text, out.Name)
	}
	if name := firstNonEmpty(out.Name, out.Source.URL, out.Source.Path, out.Text); name != "" {
		label += ": " + name
	}
	return "[" + label + "]"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
