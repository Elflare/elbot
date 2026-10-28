package share

import (
	"os"
	"path/filepath"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/workspace"
)

func TestBuildReportOutputsKeepsHTTPMediaURL(t *testing.T) {
	outputs, err := BuildReportOutputs("报告", []delivery.Output{{Kind: delivery.KindImage, Source: delivery.Source{URL: "https://example.com/chart.png"}, Name: "chart.png"}}, workspace.PathScope{})
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

func TestBuildReportOutputsUsesTaskPathScope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(path, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	scope := workspace.PathScope{Dir: dir, Restricted: true}
	outputs, err := BuildReportOutputs("", []delivery.Output{{Kind: delivery.KindFile, Source: delivery.Source{Path: "report.txt"}}}, scope)
	if err != nil || len(outputs) != 1 || outputs[0].Source.Path != path {
		t.Fatalf("report outputs=%+v err=%v", outputs, err)
	}
	if _, err := ParseJSONResult(`{"report_segments":[{"type":"file","url":"../outside"}]}`); err == nil {
		t.Fatal("report escaped task scope")
	}
}
