package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/platform"
	"elbot/internal/tool"
)

func TestSendFileAssessRiskExternalPath(t *testing.T) {
	manager := NewFileManager(filepath.Join(t.TempDir(), "sandbox"))
	sendFile := NewSendFileTool(manager)
	args, _ := json.Marshal(map[string]any{"path": filepath.Join(t.TempDir(), "report.txt")})
	assessment, err := sendFile.AssessRisk(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Level != tool.RiskHigh {
		t.Fatalf("risk = %s, want high", assessment.Level)
	}
}

func TestSendFileAssessRiskBackgroundAbsolutePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox")
	manager := NewFileManager(root)
	sendFile := NewSendFileTool(manager)
	args, _ := json.Marshal(map[string]any{"path": filepath.Join(t.TempDir(), "report.txt")})
	ctx := tool.WithSandboxContext(context.Background(), tool.SandboxContext{Root: root, Dir: filepath.Join(root, "cron"), Background: true, BackgroundKind: tool.BackgroundKindCron})
	assessment, err := sendFile.AssessRisk(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Level != tool.RiskMedium {
		t.Fatalf("risk = %s, want medium", assessment.Level)
	}
}

func TestSendFileSendsSandboxFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox")
	cronDir := filepath.Join(root, "cron")
	if err := os.MkdirAll(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cronDir, "report.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewFileManager(root)
	sendFile := NewSendFileTool(manager)
	args, _ := json.Marshal(map[string]any{"file": "report.txt"})
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qqonebot"})
	ctx = tool.WithSandboxContext(ctx, tool.SandboxContext{Root: root, Dir: cronDir, Background: true, BackgroundKind: tool.BackgroundKindCron})
	result, err := sendFile.Call(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Outputs) != 1 || result.Outputs[0].Kind != delivery.KindFile {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if result.Outputs[0].Target.Platform != "qqonebot" || !result.Outputs[0].Target.Superadmins {
		t.Fatalf("target = %#v", result.Outputs[0].Target)
	}
	sentPath := result.Outputs[0].Source.Path
	if sentPath != filepath.Join(cronDir, "report.txt") {
		t.Fatalf("sent path = %q, want cron file", sentPath)
	}
	data, err := os.ReadFile(sentPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("file content = %q", data)
	}
}

func TestSendFileSendsExternalFileDirectly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox")
	source := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(source, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewFileManager(root)
	sendFile := NewSendFileTool(manager)
	args, _ := json.Marshal(map[string]any{"file": source})
	result, err := sendFile.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	sentPath := result.Outputs[0].Source.Path
	if sentPath != source {
		t.Fatalf("sent path = %q, want source", sentPath)
	}
}

func TestNormalizeLocalPathConvertsMSYSWindowsPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("MSYS path conversion is Windows-only")
	}
	got := normalizeLocalPath("/c/Users/Cirno/dev/elbot/test.txt")
	if !strings.HasPrefix(got, `C:\Users\Cirno\dev\elbot`) {
		t.Fatalf("normalized path = %q", got)
	}
}

func TestSendFileBackgroundSendsAbsolutePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox")
	file := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	sendFile := NewSendFileTool(NewFileManager(root))
	args, _ := json.Marshal(map[string]any{"path": file})
	ctx := tool.WithSandboxContext(context.Background(), tool.SandboxContext{Root: root, Dir: filepath.Join(root, "cron"), Background: true, BackgroundKind: tool.BackgroundKindCron})
	result, err := sendFile.Call(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs[0].Source.Path != file {
		t.Fatalf("sent path = %q, want %q", result.Outputs[0].Source.Path, file)
	}
}
