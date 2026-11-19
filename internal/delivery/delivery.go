package delivery

import (
	"context"
	"fmt"
	"log/slog"
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
	Platform    string               `json:"platform,omitempty"`
	ChatType    contextinfo.ChatType `json:"chat_type,omitempty" toml:"chat_type"` // 会话类型。
	ChatID      string               `json:"id,omitempty"`
	Superadmins bool                 `json:"superadmins,omitempty"`
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
	return t == (Target{})
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
	ID      string `json:"id"`      // 操作标识，不作为授权凭据。
	Label   string `json:"label"`   // 用户看到的按钮文字。
	Command string `json:"command"` // 点击后提交的完整命令，由业务层提供。
}

type Output struct {
	ShowActionHints bool `json:"-"` // Show typed commands even when buttons are available.
	Kind            Kind
	Text            string
	// Blocks preserves typed message content until the receiving platform renders it.
	Blocks                   []ContentBlock
	Name                     string
	EmoticonID               string
	ReplyToPlatformMessageID string
	Source                   Source
	Target                   Target       // 输出意图目标，由消费层解析后显式传给 SendTo；发送层不读取此字段。
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
	var immediate, deferred []Output
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
	ChatType          contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID            string
	OutputIndexes     []int
}

type Receipt struct {
	PlatformMessageIDs []string
	SentMessages       []SentMessage
}

// MessageSender sends one logical message, represented by ordered output segments.
// Receipt preserves actual delivery facts, including partial completion on error.
// SendTo uses only its target argument: an empty target selects the current conversation.
type MessageSender interface {
	SendTo(ctx context.Context, target Target, outputs []Output) (Receipt, error)
}

// LogSendResult asynchronously publishes a chat output's source-owned failure.
func LogSendResult(ctx context.Context, target Target, out Output, err error) {
	if err == nil {
		return
	}
	record := globalevents.LogRecord{
		Category: globalevents.LogCategoryLog, Level: slog.LevelWarn,
		EventName: "chat_output_failed", Module: "delivery", ResultStatus: globalevents.ResultFailed,
		Content:  fmt.Sprintf("chat output failed: platform=%s hook=%q hook_point=%q hook_mode=%q\n%s", target.Platform, strings.TrimSpace(out.Delivery.HookName), strings.TrimSpace(out.Delivery.HookPoint), strings.TrimSpace(out.Delivery.HookMode), err),
		Identity: globalevents.LogIdentityFromContext(ctx),
	}
	// Cancellation is quiet only when no independent failure is present.
	if globalevents.LogStatus(err) == globalevents.ResultCanceled {
		record.Level, record.ResultStatus = slog.LevelInfo, globalevents.ResultCanceled
	}
	globalevents.AsyncLog(ctx, record)
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

// ValidateOutputsTarget resolves a batch's target at its consuming boundary.
// Empty targets inherit the batch target; distinct nonempty targets are invalid.
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

func FallbackText(out Output) string {
	var label string
	switch out.Kind {
	case KindText:
		return OutputBody(out, false)
	case KindEmoticon:
		return fmt.Sprintf("[表情: %s]", strings.TrimSpace(out.Name))
	case KindAt:
		return fmt.Sprintf("@%s", strings.TrimSpace(out.Name))
	case KindReply:
		text := out.Text
		if len(out.Blocks) > 0 {
			text = ContentBlocksText(out.Blocks)
		}
		return fmt.Sprintf("[引用消息 %s]\n%s", strings.TrimSpace(out.ReplyToPlatformMessageID), text)
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

func OutputBody(out Output, buttons bool) string {
	body := out.Text
	if len(out.Blocks) > 0 {
		body = ContentBlocksText(out.Blocks)
	}
	if !buttons || out.ShowActionHints {
		body = WithActionHints(body, out.Actions)
	}
	return body
}
