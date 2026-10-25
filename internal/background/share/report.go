package share

import (
	"fmt"
	"os"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/workspace"
)

func PathInstruction() string {
	return "后台任务的本地附件路径必须基于任务工作目录，使用相对路径；不得使用绝对路径、~、冒号或 .. 路径段。"
}

func BuildReportOutputs(report string, segments []llm.MessageSegment, scope workspace.PathScope) ([]delivery.Output, error) {
	outputs := []delivery.Output{}
	if text := strings.TrimSpace(report); text != "" {
		outputs = append(outputs, delivery.Text(text))
	}
	for i, segment := range segments {
		out, err := BuildReportSegmentOutput(segment, scope)
		if err != nil {
			return nil, fmt.Errorf("report segment %d: %w", i, err)
		}
		outputs = append(outputs, out)
	}
	return outputs, nil
}

func BuildReportSegmentOutput(segment llm.MessageSegment, scope workspace.PathScope) (delivery.Output, error) {
	var kind delivery.Kind
	switch segment.Type {
	case llm.SegmentImage:
		kind = delivery.KindImage
	case llm.SegmentFile:
		kind = delivery.KindFile
	default:
		return delivery.Output{}, fmt.Errorf("unsupported type %q", segment.Type)
	}

	out := delivery.Output{Kind: kind, Name: segment.Name, Source: delivery.Source{MIMEType: segment.MIMEType}}
	if segment.MediaID != "" {
		if segment.URL != "" || !media.ValidID(segment.MediaID) {
			return delivery.Output{}, fmt.Errorf("invalid or mixed report media source")
		}
		out.Source.MediaID = segment.MediaID
		return out, nil
	}
	if delivery.IsHTTPMediaSource(segment.URL) {
		out.Source.URL = strings.TrimSpace(segment.URL)
		return out, nil
	}
	path, err := workspace.ResolveSandboxRelativePath(scope, segment.URL)
	if err != nil {
		return delivery.Output{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return delivery.Output{}, fmt.Errorf("stat: %w", err)
	}
	if info.IsDir() {
		return delivery.Output{}, fmt.Errorf("path is a directory")
	}
	out.Source.Path = path
	return out, nil
}
