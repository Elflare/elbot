package output

import (
	"context"
	"fmt"
	"log/slog"
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
	MetaHookPoint = "hook.point"
	MetaHookName  = "hook.name"
	MetaHookMode  = "hook.mode"
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

type Sender interface {
	SendChat(ctx context.Context, output Output) error
	SendNotice(ctx context.Context, target Target, output Output) error
}

type Manager struct {
	Sender Sender
	Logger *slog.Logger
}

func NewManager(sender Sender, logger *slog.Logger) Manager {
	return Manager{Sender: sender, Logger: logger}
}

func (m Manager) SendAll(ctx context.Context, outputs []Output) error {
	for _, out := range outputs {
		if err := m.SendNotice(ctx, out.Target, out); err != nil {
			return err
		}
	}
	return nil
}

func (m Manager) SendChat(ctx context.Context, out Output) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.Sender == nil {
		return fmt.Errorf("output sender is not configured")
	}
	return m.Sender.SendChat(ctx, out)
}

func (m Manager) SendNotice(ctx context.Context, target Target, out Output) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.Sender == nil {
		return fmt.Errorf("output sender is not configured")
	}
	if !target.Empty() {
		out.Target = target
	} else if !out.Target.Empty() {
		target = out.Target
	}
	if err := m.Sender.SendNotice(ctx, target, out); err != nil {
		if m.Logger != nil {
			attrs := outputLogAttrs(out, "kind", out.Kind, "name", out.Name, "platform", target.Platform, "error", err.Error())
			m.Logger.WarnContext(ctx, "notice output failed", attrs...)
		}
		return wrapOutputSourceError(out, err)
	}
	return nil
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
		return ensureTrailingNewline(out.AltText)
	}
	switch out.Kind {
	case KindText:
		return ensureTrailingNewline(out.Text)
	case KindEmoticon:
		name := strings.TrimSpace(out.Name)
		if name == "" {
			name = strings.TrimSpace(out.Text)
		}
		if name == "" {
			return ""
		}
		return fmt.Sprintf("[表情: %s]\n", name)
	case KindAt:
		name := strings.TrimSpace(out.Name)
		if name == "" {
			name = strings.TrimSpace(out.Text)
		}
		if name == "" {
			return ""
		}
		return fmt.Sprintf("@%s\n", name)
	case KindReply:
		replyID := strings.TrimSpace(out.ReplyToPlatformMessageID)
		if replyID == "" {
			return ensureTrailingNewline(out.Text)
		}
		return fmt.Sprintf("[引用消息 %s]\n%s", replyID, ensureTrailingNewline(out.Text))
	case KindImage:
		label := firstNonEmpty(out.Name, out.Source.URL, out.Source.Path, out.Text)
		if label == "" {
			return "[图片]\n"
		}
		return fmt.Sprintf("[图片: %s]\n", label)
	case KindFile:
		label := firstNonEmpty(out.Name, out.Source.URL, out.Source.Path, out.Text)
		if label == "" {
			return "[文件]\n"
		}
		return fmt.Sprintf("[文件: %s]\n", label)
	default:
		return ensureTrailingNewline(firstNonEmpty(out.Text, out.Name, out.AltText))
	}
}

func ensureTrailingNewline(text string) string {
	if text == "" || strings.HasSuffix(text, "\n") {
		return text
	}
	return text + "\n"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
