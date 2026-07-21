package output

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"

	"elbot/internal/delivery"
)

const MaxBase64Bytes = 10 * 1024 * 1024

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
	Platform      string `toml:"platform" json:"platform,omitempty"`
	ScopeID       string `toml:"scope_id" json:"scope_id,omitempty"`
	PrivateUserID string `toml:"private_user_id" json:"private_user_id,omitempty"`
	GroupID       string `toml:"group_id" json:"group_id,omitempty"`
	Superadmins   bool   `toml:"superadmins" json:"superadmins,omitempty"`
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
	target := group.Target
	if target.Empty() {
		target = opts.DefaultTarget
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
		out, err := buildSegment(spec, opts.BaseDir, target.Delivery(), timing)
		if err != nil {
			return nil, fmt.Errorf("outputs[%d]: %w", i, err)
		}
		outputs = append(outputs, out)
	}
	return outputs, nil
}

func (t Target) Empty() bool {
	return strings.TrimSpace(t.Platform) == "" && strings.TrimSpace(t.ScopeID) == "" && strings.TrimSpace(t.PrivateUserID) == "" && strings.TrimSpace(t.GroupID) == "" && !t.Superadmins
}

func (t Target) Delivery() delivery.Target {
	return delivery.Target{Platform: strings.TrimSpace(t.Platform), ScopeID: strings.TrimSpace(t.ScopeID), PrivateUserID: strings.TrimSpace(t.PrivateUserID), GroupID: strings.TrimSpace(t.GroupID), Superadmins: t.Superadmins}
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
		if err := rejectFields(spec, "url", spec.URL, "path", spec.Path, "base64", spec.Base64, "name", spec.Name, "mime_type", spec.MIMEType, "user_id", spec.UserID, "message_id", spec.MessageID, "emoticon_id", spec.EmoticonID); err != nil {
			return out, err
		}
		out = delivery.Text(spec.Text)
	case delivery.KindImage, delivery.KindFile, delivery.KindRecord:
		if err := rejectFields(spec, "user_id", spec.UserID, "message_id", spec.MessageID, "emoticon_id", spec.EmoticonID); err != nil {
			return out, err
		}
		source, err := buildMediaSource(spec, baseDir)
		if err != nil {
			return out, err
		}
		out = delivery.Output{Kind: kind, Text: spec.Text, Name: spec.Name, Source: source}
	case delivery.KindEmoticon:
		if err := rejectFields(spec, "url", spec.URL, "path", spec.Path, "base64", spec.Base64, "mime_type", spec.MIMEType, "user_id", spec.UserID, "message_id", spec.MessageID); err != nil {
			return out, err
		}
		if spec.EmoticonID == "" {
			return out, fmt.Errorf("emoticon_id is required for emoticon output")
		}
		out = delivery.Emoticon(spec.EmoticonID, spec.Name, spec.Text)
	case delivery.KindAt:
		if err := rejectFields(spec, "text", spec.Text, "url", spec.URL, "path", spec.Path, "base64", spec.Base64, "name", spec.Name, "mime_type", spec.MIMEType, "message_id", spec.MessageID, "emoticon_id", spec.EmoticonID); err != nil {
			return out, err
		}
		if spec.UserID == "" {
			return out, fmt.Errorf("user_id is required for at output")
		}
		out = delivery.At(spec.UserID)
	case delivery.KindReply:
		if err := rejectFields(spec, "url", spec.URL, "path", spec.Path, "base64", spec.Base64, "name", spec.Name, "mime_type", spec.MIMEType, "user_id", spec.UserID, "emoticon_id", spec.EmoticonID); err != nil {
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

func buildMediaSource(spec Segment, baseDir string) (delivery.Source, error) {
	count := 0
	for _, value := range []string{spec.Path, spec.URL, spec.Base64} {
		if value != "" {
			count++
		}
	}
	if count != 1 {
		return delivery.Source{}, fmt.Errorf("image/file/record output must provide exactly one of path, url or base64")
	}
	source := delivery.Source{MIMEType: spec.MIMEType}
	if spec.Path != "" {
		if strings.Contains(spec.Path, "://") {
			return source, fmt.Errorf("path must be a filesystem path, not a URI")
		}
		source.Path = spec.Path
		if !filepath.IsAbs(source.Path) && strings.TrimSpace(baseDir) != "" {
			source.Path = filepath.Join(baseDir, source.Path)
		}
		return source, nil
	}
	if spec.URL != "" {
		u, err := url.Parse(spec.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return source, fmt.Errorf("url must be an absolute HTTP(S) URL")
		}
		source.URL = spec.URL
		return source, nil
	}
	if base64.StdEncoding.DecodedLen(len(spec.Base64)) > MaxBase64Bytes {
		return source, fmt.Errorf("base64 output exceeds 10 MiB decoded limit; write large media to a file and return outputs[].path or outputs[].url instead")
	}
	data, err := base64.StdEncoding.DecodeString(spec.Base64)
	if err != nil {
		return source, fmt.Errorf("decode base64 output: %w", err)
	}
	if len(data) > MaxBase64Bytes {
		return source, fmt.Errorf("base64 output exceeds 10 MiB decoded limit; write large media to a file and return outputs[].path or outputs[].url instead")
	}
	source.Data = data
	return source, nil
}

func trimSegment(spec Segment) Segment {
	spec.Kind = strings.TrimSpace(spec.Kind)
	spec.Text = strings.TrimSpace(spec.Text)
	spec.URL = strings.TrimSpace(spec.URL)
	spec.Path = strings.TrimSpace(spec.Path)
	spec.Base64 = strings.TrimSpace(spec.Base64)
	spec.Name = strings.TrimSpace(spec.Name)
	spec.MIMEType = strings.TrimSpace(spec.MIMEType)
	spec.UserID = strings.TrimSpace(spec.UserID)
	spec.MessageID = strings.TrimSpace(spec.MessageID)
	spec.EmoticonID = strings.TrimSpace(spec.EmoticonID)
	return spec
}

func rejectFields(_ Segment, fields ...string) error {
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i+1] != "" {
			return fmt.Errorf("field %s is not supported for this output kind", fields[i])
		}
	}
	return nil
}
