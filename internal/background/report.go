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
		path, err := tool.ResolveSandboxRelativePath(sandbox, segment.URL)
		if err != nil {
			return nil, fmt.Errorf("report segment %d: %w", i, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("report segment %d stat: %w", i, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("report segment %d path is a directory", i)
		}
		switch segment.Type {
		case llm.SegmentImage:
			out := delivery.ImagePath(path)
			out.Name = segment.Name
			out.Source.MIMEType = segment.MIMEType
			outputs = append(outputs, out)
		case llm.SegmentFile:
			out := delivery.FilePath(path)
			out.Name = segment.Name
			out.Source.MIMEType = segment.MIMEType
			outputs = append(outputs, out)
		default:
			return nil, fmt.Errorf("report segment %d has unsupported type %q", i, segment.Type)
		}
	}
	return outputs, nil
}
