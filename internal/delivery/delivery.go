package delivery

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

type Kind string

const (
	KindText     Kind = "text"
	KindEmoticon Kind = "emoticon"
	KindImage    Kind = "image"
	KindFile     Kind = "file"
	KindAt       Kind = "at"
	KindReply    Kind = "reply"
)

type Target struct {
	Platform      string
	ScopeID       string
	PrivateUserID string
	GroupID       string
	Superadmins   bool
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

// IsDirectMediaSource reports whether value already declares a media source scheme.
func IsDirectMediaSource(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "base64://") ||
		strings.HasPrefix(value, "file://") ||
		strings.HasPrefix(value, "http://") ||
		strings.HasPrefix(value, "https://")
}

// IsHTTPMediaSource reports whether value is an HTTP(S) media source.
func IsHTTPMediaSource(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}

// IsBase64MediaSource reports whether value is a base64:// media source.
func IsBase64MediaSource(value string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "base64://")
}

// IsFileMediaSource reports whether value is a file:// media source.
func IsFileMediaSource(value string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "file://")
}

// FileURIToPath converts file:// media source values to local filesystem paths.
func FileURIToPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !IsFileMediaSource(value) {
		return value, nil
	}
	u, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("parse file uri: %w", err)
	}
	path := u.Path
	if path == "" {
		return "", fmt.Errorf("file uri path is empty")
	}
	if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	if u.Host != "" {
		path = "//" + u.Host + path
	}
	return filepath.FromSlash(path), nil
}

type Output struct {
	Kind                     Kind
	Text                     string
	Name                     string
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

func Emoticon(name string) Output {
	name = strings.TrimSpace(name)
	out := Output{Kind: KindEmoticon, Name: name}
	if name != "" {
		out.AltText = "[表情: " + name + "]"
	}
	return out
}

func EmoticonPath(name, path string) Output {
	out := Emoticon(name)
	out.Source.Path = path
	return out
}

func ImagePath(path string) Output {
	return Output{Kind: KindImage, Source: Source{Path: path}}
}

func FilePath(path string) Output {
	return Output{Kind: KindFile, Source: Source{Path: path}}
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

// MessageSender sends chat messages and notifications through a platform.
type MessageSender interface {
	SendChat(ctx context.Context, out Output) (Receipt, error)
	SendNotice(ctx context.Context, target Target, out Output) (Receipt, error)
}

// ContextSender can send a reply using routing information carried by ctx.
type ContextSender interface {
	MessageSender
}

type Sender interface {
	SendChat(ctx context.Context, output Output) (Receipt, error)
	SendNotice(ctx context.Context, target Target, output Output) (Receipt, error)
}

type Manager struct {
	Sender Sender
	Logger *slog.Logger
}

func NewManager(sender Sender, logger *slog.Logger) Manager {
	return Manager{Sender: sender, Logger: logger}
}

func (m Manager) SendNotices(ctx context.Context, outputs []Output) error {
	for _, out := range outputs {
		if _, err := m.SendNotice(ctx, out.Target, out); err != nil {
			return err
		}
	}
	return nil
}

func (m Manager) SendChat(ctx context.Context, out Output) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if m.Sender == nil {
		return Receipt{}, fmt.Errorf("output sender is not configured")
	}
	return m.Sender.SendChat(ctx, out)
}

func (m Manager) SendNotice(ctx context.Context, target Target, out Output) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if m.Sender == nil {
		return Receipt{}, fmt.Errorf("output sender is not configured")
	}
	if !target.Empty() {
		out.Target = target
	} else if !out.Target.Empty() {
		target = out.Target
	}
	receipt, err := m.Sender.SendNotice(ctx, target, out)
	if err != nil {
		if m.Logger != nil {
			attrs := outputLogAttrs(out, "kind", out.Kind, "name", out.Name, "platform", target.Platform, "error", err.Error())
			m.Logger.WarnContext(ctx, "notice output failed", attrs...)
		}
		return Receipt{}, wrapOutputSourceError(out, err)
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
