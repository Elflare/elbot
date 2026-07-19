package background

import (
	"fmt"
	"os"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/tool"
)

func PathInstruction() string {
	return tool.BackgroundPathInstruction()
}

func BuildReportOutputs(report string, segments []llm.MessageSegment, sandbox tool.SandboxContext) ([]delivery.Output, error) {
	outputs := []delivery.Output{}
	if text := strings.TrimSpace(report); text != "" {
		outputs = append(outputs, delivery.Text(text))
	}
	for i, segment := range segments {
		out, err := BuildReportSegmentOutput(segment, sandbox)
		if err != nil {
			return nil, fmt.Errorf("report segment %d: %w", i, err)
		}
		outputs = append(outputs, out)
	}
	return outputs, nil
}

func BuildReportSegmentOutput(segment llm.MessageSegment, sandbox tool.SandboxContext) (delivery.Output, error) {
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
	if delivery.IsHTTPMediaSource(segment.URL) {
		out.Source.URL = strings.TrimSpace(segment.URL)
		return out, nil
	}
	path, err := tool.ResolveSandboxRelativePath(sandbox, segment.URL)
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
