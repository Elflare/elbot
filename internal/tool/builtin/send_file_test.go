package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/platform"
	"elbot/internal/tool"
)

func TestSendFileAssessRiskExternalPath(t *testing.T) {
	manager := NewFileManager(filepath.Join(t.TempDir(), "sandbox"), config.FileDeliveryConfig{})
	sendFile := NewSendFileTool(manager)
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte("report"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path})
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
	manager := NewFileManager(root, config.FileDeliveryConfig{})
	sendFile := NewSendFileTool(manager)
	args, _ := json.Marshal(map[string]any{"path": filepath.Join(t.TempDir(), "report.txt")})
	ctx := tool.WithSandboxContext(context.Background(), tool.SandboxContext{Root: root, Dir: filepath.Join(root, "cron"), Background: true, BackgroundKind: tool.BackgroundKindCron})
	_, err := sendFile.AssessRisk(ctx, tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "background path must be relative") {
		t.Fatalf("expected background absolute path rejection, got %v", err)
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
	manager := NewFileManager(root, config.FileDeliveryConfig{})
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

func TestSendFileUsesWorkspaceRelativePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox")
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "report.txt"), []byte("workspace"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewFileManager(root, config.FileDeliveryConfig{})
	sendFile := NewSendFileTool(manager)
	args, _ := json.Marshal(map[string]any{"file": "report.txt"})
	ctx := tool.WithWorkspaceStore(context.Background(), &testWorkspaceStore{dir: workspace})
	result, err := sendFile.Call(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs[0].Source.Path; got != filepath.Join(workspace, "report.txt") {
		t.Fatalf("sent path = %q", got)
	}
}

func TestSendFileSendsExternalFileDirectly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox")
	source := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(source, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewFileManager(root, config.FileDeliveryConfig{})
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

func TestSendFileBackgroundRejectsAbsolutePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox")
	file := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	sendFile := NewSendFileTool(NewFileManager(root, config.FileDeliveryConfig{}))
	args, _ := json.Marshal(map[string]any{"path": file})
	ctx := tool.WithSandboxContext(context.Background(), tool.SandboxContext{Root: root, Dir: filepath.Join(root, "cron"), Background: true, BackgroundKind: tool.BackgroundKindCron})
	_, err := sendFile.Call(ctx, tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "background path must be relative") {
		t.Fatalf("expected background absolute path rejection, got %v", err)
	}
}

func TestSendFileRejectsOversizedBase64File(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox")
	file := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(file, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewFileManager(root, config.FileDeliveryConfig{MaxDirectBase64Bytes: 4, Backend: "base64"})
	sendFile := NewSendFileTool(manager)
	args, _ := json.Marshal(map[string]any{"path": file})
	_, err := sendFile.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil {
		t.Fatal("expected oversized file error")
	}
}
