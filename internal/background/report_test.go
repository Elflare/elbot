package background

import (
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/tool"
)

func TestBuildReportOutputsKeepsHTTPMediaURL(t *testing.T) {
	outputs, err := BuildReportOutputs("报告", []llm.MessageSegment{{Type: llm.SegmentImage, URL: "https://example.com/chart.png", Name: "chart.png"}}, tool.SandboxContext{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("BuildReportOutputs: %v", err)
	}
	if len(outputs) != 2 || outputs[0].Kind != delivery.KindText || outputs[1].Kind != delivery.KindImage {
		t.Fatalf("outputs = %#v", outputs)
	}
	if outputs[1].Source.URL != "https://example.com/chart.png" || outputs[1].Source.Path != "" {
		t.Fatalf("media source = %#v", outputs[1].Source)
	}
}
