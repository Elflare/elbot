// 将插件输出描述转换为投递输出。

package output

import (
	"bytes"
	"elbot/internal/contextinfo"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/media"
)

const MaxBase64Bytes = 10 * 1024 * 1024

var ErrBase64TooLarge = errors.New("exceeds 10 MiB decoded limit")

type Segment struct {
	Kind       string `toml:"kind" json:"kind"`
	Text       string `toml:"text" json:"text,omitempty"`
	URL        string `toml:"url" json:"url,omitempty"`
	Path       string `toml:"path" json:"path,omitempty"`
	Base64     string `toml:"base64" json:"base64,omitempty"`
	Name       string `toml:"name" json:"name,omitempty"`
	MIMEType   string `toml:"mime_type" json:"mime_type,omitempty"`
	UserID     string `toml:"user_id" json:"user_id,omitempty"`
	MessageID  string `toml:"message_id" json:"message_id,omitempty"`
	EmoticonID string `toml:"emoticon_id" json:"emoticon_id,omitempty"`
}

type Target struct {
	Platform    string               `toml:"platform" json:"platform,omitempty"`
	ChatType    contextinfo.ChatType `json:"chat_type,omitempty" toml:"chat_type"` // 会话类型。
	ChatID      string               `toml:"id" json:"id,omitempty"`
	Superadmins bool                 `toml:"superadmins" json:"superadmins,omitempty"`
}

type Group struct {
	Outputs []Segment `json:"outputs"`
	Target  Target    `json:"target,omitempty"`
	Timing  string    `json:"timing,omitempty"`
}

type BuildOptions struct {
	BaseDir       string
	DefaultTarget Target
	DefaultTiming string
}

func DecodeJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func BuildGroup(group Group, opts BuildOptions) ([]delivery.Output, error) {
	target := group.Target.Delivery()
	if group.Target.Empty() {
		target = opts.DefaultTarget.Delivery()
	}
	timing := strings.TrimSpace(group.Timing)
	if timing == "" {
		timing = strings.TrimSpace(opts.DefaultTiming)
	}
	if err := delivery.ValidateDeliveryTiming(timing); err != nil {
		return nil, err
	}
	outputs := make([]delivery.Output, 0, len(group.Outputs))
	for i, spec := range group.Outputs {
		out, err := buildSegment(spec, opts.BaseDir, target, timing)
		if err != nil {
			return nil, fmt.Errorf("outputs[%d]: %w", i, err)
		}
		outputs = append(outputs, out)
	}
	return outputs, nil
}

func (t Target) Empty() bool {
	return t == (Target{})
}

func (t Target) Delivery() delivery.Target {
	return delivery.Target{Platform: t.Platform, ChatType: t.ChatType, ChatID: t.ChatID, Superadmins: t.Superadmins}
}

func buildSegment(spec Segment, baseDir string, target delivery.Target, timing string) (delivery.Output, error) {
	spec = trimSegment(spec)
	kind := delivery.Kind(spec.Kind)
	if kind == "" {
		kind = delivery.KindText
	}
	var out delivery.Output
	switch kind {
	case delivery.KindText:
		if err := rejectFields("url", spec.URL, "path", spec.Path, "base64", spec.Base64, "name", spec.Name, "mime_type", spec.MIMEType, "user_id", spec.UserID, "message_id", spec.MessageID, "emoticon_id", spec.EmoticonID); err != nil {
			return out, err
		}
		out = delivery.Text(spec.Text)
	case delivery.KindImage, delivery.KindFile, delivery.KindRecord:
		if err := rejectFields("user_id", spec.UserID, "message_id", spec.MessageID, "emoticon_id", spec.EmoticonID); err != nil {
			return out, err
		}
		source, err := buildMediaSource(spec, baseDir)
		if err != nil {
			return out, err
		}
		out = delivery.Output{Kind: kind, Text: spec.Text, Name: spec.Name, Source: source}
	case delivery.KindEmoticon:
		if err := rejectFields("url", spec.URL, "path", spec.Path, "base64", spec.Base64, "mime_type", spec.MIMEType, "user_id", spec.UserID, "message_id", spec.MessageID); err != nil {
			return out, err
		}
		if spec.EmoticonID == "" {
			return out, fmt.Errorf("emoticon_id is required for emoticon output")
		}
		if spec.Name == "" {
			return out, fmt.Errorf("name is required for emoticon output")
		}
		out = delivery.Emoticon(spec.EmoticonID, spec.Name)
	case delivery.KindAt:
		if err := rejectFields("text", spec.Text, "url", spec.URL, "path", spec.Path, "base64", spec.Base64, "name", spec.Name, "mime_type", spec.MIMEType, "message_id", spec.MessageID, "emoticon_id", spec.EmoticonID); err != nil {
			return out, err
		}
		if spec.UserID == "" {
			return out, fmt.Errorf("user_id is required for at output")
		}
		out = delivery.At(spec.UserID)
	case delivery.KindReply:
		if err := rejectFields("url", spec.URL, "path", spec.Path, "base64", spec.Base64, "name", spec.Name, "mime_type", spec.MIMEType, "user_id", spec.UserID, "emoticon_id", spec.EmoticonID); err != nil {
			return out, err
		}
		if spec.MessageID == "" {
			return out, fmt.Errorf("message_id is required for reply output")
		}
		out = delivery.Reply(spec.MessageID, spec.Text)
	default:
		return out, fmt.Errorf("unsupported output kind %q", spec.Kind)
	}
	out.Target = target
	return delivery.WithDeliveryTiming(out, timing), nil
}

// singleMediaSource returns the declared field, or media_id for an explicit media reference.
func singleMediaSource(path, url, b64 string) (kind, value string) {
	for _, field := range [...]struct{ kind, value string }{{"path", path}, {"url", url}, {"base64", b64}} {
		if field.value == "" {
			continue
		}
		if kind != "" {
			return "", ""
		}
		kind, value = field.kind, field.value
	}
	if len(value) >= len(media.IDPrefix) && strings.EqualFold(value[:len(media.IDPrefix)], media.IDPrefix) {
		return "media_id", value
	}
	return kind, value
}

func buildMediaSource(spec Segment, baseDir string) (delivery.Source, error) {
	kind, value := singleMediaSource(spec.Path, spec.URL, spec.Base64)
	source := delivery.Source{MIMEType: spec.MIMEType}
	switch kind {
	case "":
		return delivery.Source{}, fmt.Errorf("image/file/record output must provide exactly one of path, url or base64")
	case "media_id":
		if !media.ValidID(value) {
			return source, fmt.Errorf("invalid media ID")
		}
		source.MediaID = value
	case "path":
		if strings.Contains(value, "://") {
			return source, fmt.Errorf("path must be a filesystem path, not a URI")
		}
		if !filepath.IsAbs(value) && strings.TrimSpace(baseDir) != "" {
			value = filepath.Join(baseDir, value)
		}
		source.Path = value
	case "url":
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return source, fmt.Errorf("url must be an absolute HTTP(S) URL")
		}
		source.URL = value
	case "base64":
		data, err := decodeBase64(value, "base64 output", "; write large media to a file and return outputs[].path or outputs[].url instead")
		source.Data = data
		return source, err
	}
	return source, nil
}

func decodeBase64(value, label, hint string) ([]byte, error) {
	data, err := DecodeBase64(value)
	switch err {
	case nil:
		return data, nil
	case ErrBase64TooLarge:
		return nil, fmt.Errorf("%s %w%s", label, err, hint)
	default:
		return nil, fmt.Errorf("decode %s: %w", label, err)
	}
}

// DecodeBase64 bounds the allocation and checks the actual decoded size.
func DecodeBase64(value string) ([]byte, error) {
	n, limit := len(value), base64.StdEncoding.EncodedLen(MaxBase64Bytes)
	if n > limit {
		n -= strings.Count(value, "\r") + strings.Count(value, "\n")
	}
	if n <= limit {
		data := make([]byte, base64.StdEncoding.DecodedLen(n))
		size, err := base64.StdEncoding.Decode(data, []byte(value))
		if err != nil {
			return nil, err
		}
		if size <= MaxBase64Bytes {
			return data[:size], nil
		}
	}
	return nil, ErrBase64TooLarge
}

func trimSegment(spec Segment) Segment {
	spec.Kind = strings.TrimSpace(spec.Kind)
	spec.Text = strings.TrimSpace(spec.Text)
	spec.URL = strings.TrimSpace(spec.URL)
	spec.Path = strings.TrimSpace(spec.Path)
	spec.Base64 = strings.TrimSpace(spec.Base64)
	spec.Name = strings.TrimSpace(spec.Name)
	spec.MIMEType = strings.TrimSpace(spec.MIMEType)
	spec.MessageID = strings.TrimSpace(spec.MessageID)
	spec.EmoticonID = strings.TrimSpace(spec.EmoticonID)
	return spec
}

func rejectFields(fields ...string) error {
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i+1] != "" {
			return fmt.Errorf("field %s is not supported for this output kind", fields[i])
		}
	}
	return nil
}
