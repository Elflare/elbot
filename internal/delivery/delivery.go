package delivery

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
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
	MetaHookPoint      = "hook.point"
	MetaHookName       = "hook.name"
	MetaHookMode       = "hook.mode"
	MetaDeliveryTiming = "delivery.timing"
)

const (
	DeliveryImmediate      = "immediate"
	DeliveryAfterAssistant = "after_assistant"
)

func (t Target) Empty() bool {
	return strings.TrimSpace(t.Platform) == "" && strings.TrimSpace(t.ScopeID) == "" && strings.TrimSpace(t.PrivateUserID) == "" && strings.TrimSpace(t.GroupID) == "" && !t.Superadmins
}

type Source struct {
	URL      string
	Path     string
	MIMEType string
	Data     []byte
}

// IsHTTPMediaSource reports whether value is an HTTP(S) media source.
func IsHTTPMediaSource(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}

type Output struct {
	Kind                     Kind
	Text                     string
	Name                     string
	EmoticonID               string
	AltText                  string
	ReplyToPlatformMessageID string
	Source                   Source
	Target                   Target
	Meta                     map[string]any
}

func WithDeliveryTiming(out Output, timing string) Output {
	timing = strings.TrimSpace(timing)
	if timing == "" || timing == DeliveryImmediate {
		return out
	}
	if out.Meta == nil {
		out.Meta = map[string]any{}
	}
	out.Meta[MetaDeliveryTiming] = timing
	return out
}

func DeliveryTiming(out Output) string {
	timing := outputMetaString(out, MetaDeliveryTiming)
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

func Emoticon(id, name, text string) Output {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	out := Output{Kind: KindEmoticon, EmoticonID: id, Name: name, Text: strings.TrimSpace(text)}
	if name != "" {
		out.AltText = "[表情: " + name + "]"
	} else if out.Text != "" {
		out.AltText = out.Text
	}
	return out
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
	userID = strings.TrimSpace(userID)
	out := Output{Kind: KindAt, Name: userID}
	if userID != "" {
		out.AltText = "@" + userID
	}
	return out
}

func Reply(platformMessageID, text string) Output {
	return Output{Kind: KindReply, Text: text, ReplyToPlatformMessageID: strings.TrimSpace(platformMessageID)}
}

// Receipt describes platform messages produced by a send operation.
type Receipt struct {
	PlatformMessageIDs []string
}

// StreamingMessageSender is an optional platform capability for editable streaming delivery.
// Platforms can implement it with terminal replacement, message editing, or any equivalent mechanism.
type StreamingMessageSender interface {
	StartStream(ctx context.Context) (MessageStream, error)
}

// MessageStream represents one assistant message that can be appended while streaming
// and replaced with the final post-hook content.
type MessageStream interface {
	Append(ctx context.Context, text string) error
	Replace(ctx context.Context, text string) (Receipt, error)
	Finish(ctx context.Context) (Receipt, error)
}

// MessageSender sends one logical message, represented by ordered output segments.
type MessageSender interface {
	SendChat(ctx context.Context, outputs []Output) (Receipt, error)
	SendNotice(ctx context.Context, target Target, outputs []Output) (Receipt, error)
}

// ContextSender can send a reply using routing information carried by ctx.
type ContextSender interface {
	MessageSender
}

type Sender = MessageSender

type Manager struct {
	Sender Sender
	Logger *slog.Logger
}

func NewManager(sender Sender, logger *slog.Logger) Manager {
	return Manager{Sender: sender, Logger: logger}
}

func (m Manager) SendNotices(ctx context.Context, outputs []Output) error {
	_, err := m.SendNotice(ctx, Target{}, outputs)
	return err
}

func (m Manager) SendChat(ctx context.Context, outputs []Output) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if m.Sender == nil {
		return Receipt{}, fmt.Errorf("output sender is not configured")
	}
	if err := ValidateOutputs(outputs); err != nil {
		return Receipt{}, err
	}
	return m.Sender.SendChat(ctx, outputs)
}

func (m Manager) SendNotice(ctx context.Context, target Target, outputs []Output) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if m.Sender == nil || len(outputs) == 0 {
		return Receipt{}, nil
	}
	if err := ValidateOutputs(outputs); err != nil {
		return Receipt{}, err
	}
	configuredTarget, err := ValidateOutputsTarget(outputs)
	if err != nil {
		return Receipt{}, err
	}
	if target.Empty() {
		target = configuredTarget
	}
	receipt, err := m.Sender.SendNotice(ctx, target, outputs)
	if err != nil {
		if m.Logger != nil {
			attrs := outputLogAttrs(outputs[0], "platform", target.Platform, "error", err.Error())
			m.Logger.WarnContext(ctx, "notice output failed", attrs...)
		}
		return Receipt{}, wrapOutputSourceError(outputs[0], err)
	}
	return receipt, nil
}

func outputLogAttrs(out Output, attrs ...any) []any {
	if hookName := outputMetaString(out, MetaHookName); hookName != "" {
		attrs = append(attrs, "hook", hookName)
	}
	if hookPoint := outputMetaString(out, MetaHookPoint); hookPoint != "" {
		attrs = append(attrs, "hook_point", hookPoint)
	}
	if hookMode := outputMetaString(out, MetaHookMode); hookMode != "" {
		attrs = append(attrs, "hook_mode", hookMode)
	}
	return attrs
}

func wrapOutputSourceError(out Output, err error) error {
	if err == nil {
		return nil
	}
	if hookName := outputMetaString(out, MetaHookName); hookName != "" {
		return fmt.Errorf("hook %s output: %w", hookName, err)
	}
	if hookPoint := outputMetaString(out, MetaHookPoint); hookPoint != "" {
		return fmt.Errorf("hook %s output: %w", hookPoint, err)
	}
	return err
}

func outputMetaString(out Output, key string) string {
	if out.Meta == nil {
		return ""
	}
	value, ok := out.Meta[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
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

func ValidateOutputs(outputs []Output) error {
	for i, out := range outputs {
		if err := ValidateDeliveryTiming(DeliveryTiming(out)); err != nil {
			return fmt.Errorf("outputs[%d]: %w", i, err)
		}
		sourceCount := 0
		if strings.TrimSpace(out.Source.Path) != "" {
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
				return fmt.Errorf("outputs[%d]: text output cannot have a media source", i)
			}
		case KindImage, KindFile, KindRecord:
			if sourceCount != 1 {
				return fmt.Errorf("outputs[%d]: image/file/record output must have exactly one media source", i)
			}
			if path := strings.TrimSpace(out.Source.Path); strings.Contains(path, "://") {
				return fmt.Errorf("outputs[%d]: media path must be a filesystem path, not a URI", i)
			}
			if value := strings.TrimSpace(out.Source.URL); value != "" {
				u, err := url.Parse(value)
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					return fmt.Errorf("outputs[%d]: media URL must be an absolute HTTP(S) URL", i)
				}
			}
		case KindEmoticon:
			if sourceCount != 0 {
				return fmt.Errorf("outputs[%d]: emoticon output cannot have a media source", i)
			}
			if strings.TrimSpace(out.EmoticonID) == "" {
				return fmt.Errorf("outputs[%d]: emoticon output requires an emoticon ID", i)
			}
		case KindAt:
			if strings.TrimSpace(out.Name) == "" {
				return fmt.Errorf("outputs[%d]: at output requires a user ID", i)
			}
		case KindReply:
			if strings.TrimSpace(out.ReplyToPlatformMessageID) == "" {
				return fmt.Errorf("outputs[%d]: reply output requires a message ID", i)
			}
		default:
			return fmt.Errorf("outputs[%d]: unsupported output kind %q", i, out.Kind)
		}
	}
	_, err := ValidateOutputsTarget(outputs)
	return err
}

func FallbackText(out Output) string {
	if out.AltText != "" {
		return out.AltText
	}
	switch out.Kind {
	case KindText:
		return out.Text
	case KindEmoticon:
		name := strings.TrimSpace(out.Name)
		if name == "" {
			name = strings.TrimSpace(out.Text)
		}
		if name == "" {
			return ""
		}
		return fmt.Sprintf("[表情: %s]", name)
	case KindAt:
		name := strings.TrimSpace(out.Name)
		if name == "" {
			name = strings.TrimSpace(out.Text)
		}
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
		label := firstNonEmpty(out.Name, out.Source.URL, out.Source.Path, out.Text)
		if label == "" {
			return "[图片]"
		}
		return fmt.Sprintf("[图片: %s]", label)
	case KindFile:
		label := firstNonEmpty(out.Name, out.Source.URL, out.Source.Path, out.Text)
		if label == "" {
			return "[文件]"
		}
		return fmt.Sprintf("[文件: %s]", label)
	case KindRecord:
		label := firstNonEmpty(out.Name, out.Source.URL, out.Source.Path, out.Text)
		if label == "" {
			return "[语音]"
		}
		return fmt.Sprintf("[语音: %s]", label)
	default:
		return firstNonEmpty(out.Text, out.Name, out.AltText)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
