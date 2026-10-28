package share

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/workspace"
)

func ParseJSONResult(text string) (JSONResult, error) {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return JSONResult{}, fmt.Errorf("json object not found")
	}
	var raw struct {
		Completed      bool                 `json:"completed"`
		NeedReport     bool                 `json:"need_report"`
		Report         string               `json:"report"`
		ReportSegments []llm.MessageSegment `json:"report_segments"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &raw); err != nil {
		return JSONResult{}, err
	}
	result := JSONResult{Completed: raw.Completed, NeedReport: raw.NeedReport, Report: raw.Report}
	for i, segment := range raw.ReportSegments {
		var kind delivery.Kind
		switch segment.Type {
		case llm.SegmentImage:
			kind = delivery.KindImage
		case llm.SegmentFile:
			kind = delivery.KindFile
		default:
			return JSONResult{}, fmt.Errorf("report segment %d: unsupported type %q", i, segment.Type)
		}
		out := delivery.Output{Kind: kind, Name: segment.Name, Source: delivery.Source{MIMEType: segment.MIMEType}}
		value := strings.TrimSpace(segment.URL)
		if segment.MediaID != "" {
			if value != "" || !media.ValidID(segment.MediaID) {
				return JSONResult{}, fmt.Errorf("report segment %d: invalid or mixed media source", i)
			}
			out.Source.MediaID = segment.MediaID
		} else if path, err := workspace.NormalizeRestrictedPath(value); err == nil {
			out.Source.Path = path
		} else {
			u, err := url.Parse(value)
			if err != nil {
				return JSONResult{}, fmt.Errorf("report segment %d: invalid source", i)
			}
			switch strings.ToLower(u.Scheme) {
			case "media":
				if !media.ValidID(value) {
					return JSONResult{}, fmt.Errorf("report segment %d: invalid media ID", i)
				}
				out.Source.MediaID = value
			case "http", "https":
				if u.Host == "" {
					return JSONResult{}, fmt.Errorf("report segment %d: URL requires a host", i)
				}
				u.Scheme = strings.ToLower(u.Scheme)
				out.Source.URL = u.String()
			case "":
				return JSONResult{}, fmt.Errorf("report segment %d: invalid local path", i)
			default:
				return JSONResult{}, fmt.Errorf("report segment %d: unsupported source scheme %q", i, u.Scheme)
			}
		}
		result.ReportSegments = append(result.ReportSegments, out)
	}
	return result, nil
}

// MarshalJSON 只输出既有外部协议字段，不暴露内部发送结构。
func (r JSONResult) MarshalJSON() ([]byte, error) {
	segments := make([]llm.MessageSegment, 0, len(r.ReportSegments))
	for _, out := range r.ReportSegments {
		segment := llm.MessageSegment{Type: llm.MessageSegmentType(out.Kind), Name: out.Name, MIMEType: out.Source.MIMEType, MediaID: out.Source.MediaID}
		if out.Source.URL != "" {
			segment.URL = out.Source.URL
		} else {
			segment.URL = out.Source.Path
		}
		segments = append(segments, segment)
	}
	return json.Marshal(struct {
		Completed      bool                 `json:"completed"`
		NeedReport     bool                 `json:"need_report"`
		Report         string               `json:"report"`
		ReportSegments []llm.MessageSegment `json:"report_segments,omitempty"`
	}{r.Completed, r.NeedReport, r.Report, segments})
}

func DefaultJSONRetryPrompt() string {
	return "你返回的格式有误，请严格使用 JSON 格式。不能包含 Markdown 代码块，不能包含 JSON 外的任何文字。格式：{\"completed\":true,\"need_report\":false,\"report\":\"\",\"report_segments\":[]}。need_report 表示是否需要向目标平台汇报；成功、失败或阻塞都可以请求汇报。report_segments 可选，url 使用工作目录内的相对路径或直接使用 HTTP(S) URL；远程 URL 不会下载。" + PathInstruction()
}
