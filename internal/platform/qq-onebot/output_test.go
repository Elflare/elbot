package qqonebot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/output"
)

func TestOutputSegmentsFileUsesBase64(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := output.FilePath(path)
	out.Name = "report.txt"
	segments, err := outputSegments(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 || segments[0].Type != "file" {
		t.Fatalf("segments = %#v", segments)
	}
	file, _ := segments[0].Data["file"].(string)
	if !strings.HasPrefix(file, "base64://") {
		t.Fatalf("file data = %q", file)
	}
	if segments[0].Data["name"] != "report.txt" {
		t.Fatalf("name = %#v", segments[0].Data["name"])
	}
}
