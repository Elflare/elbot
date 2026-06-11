package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/output"
	"elbot/internal/platform"
	"elbot/internal/tool"
)

func TestSendFileAssessRiskExternalPath(t *testing.T) {
	manager := NewArtifactManager(filepath.Join(t.TempDir(), "sandbox"), config.ArtifactConfig{})
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

func TestSendFileAssessRiskCronExternalPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox")
	manager := NewArtifactManager(root, config.ArtifactConfig{})
	sendFile := NewSendFileTool(manager)
	args, _ := json.Marshal(map[string]any{"path": filepath.Join(t.TempDir(), "report.txt")})
	ctx := tool.WithSandboxContext(context.Background(), tool.SandboxContext{Root: root, Dir: filepath.Join(root, "cron"), ArtifactDir: filepath.Join(root, "artifact"), CronBackground: true})
	assessment, err := sendFile.AssessRisk(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Level != tool.RiskMedium {
		t.Fatalf("risk = %s, want medium", assessment.Level)
	}
}

func TestSendFileSendsSandboxFileWithoutCopying(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox")
	cronDir := filepath.Join(root, "cron")
	if err := os.MkdirAll(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cronDir, "report.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewArtifactManager(root, config.ArtifactConfig{})
	sendFile := NewSendFileTool(manager)
	args, _ := json.Marshal(map[string]any{"file": "report.txt"})
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qqonebot"})
	ctx = tool.WithSandboxContext(ctx, tool.SandboxContext{Root: root, Dir: cronDir, ArtifactDir: filepath.Join(root, "artifact"), CronBackground: true})
	result, err := sendFile.Call(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Outputs) != 1 || result.Outputs[0].Kind != output.KindFile {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if result.Outputs[0].Target.Platform != "qqonebot" || !result.Outputs[0].Target.Superadmins {
		t.Fatalf("target = %#v", result.Outputs[0].Target)
	}
	artifactPath := result.Outputs[0].Source.Path
	if artifactPath != filepath.Join(cronDir, "report.txt") {
		t.Fatalf("sent path = %q, want cron file", artifactPath)
	}
	data, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("artifact content = %q", data)
	}
}

func TestSendFileCopiesExternalFileToArtifact(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox")
	source := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(source, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewArtifactManager(root, config.ArtifactConfig{})
	sendFile := NewSendFileTool(manager)
	args, _ := json.Marshal(map[string]any{"file": source})
	result, err := sendFile.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := result.Outputs[0].Source.Path
	if !isPathWithin(artifactPath, filepath.Join(root, "artifact")) {
		t.Fatalf("artifact path %q is outside artifact dir", artifactPath)
	}
	if artifactPath == source {
		t.Fatal("external file was not copied")
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

func TestSendFileRejectsOversizedBase64File(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox")
	file := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(file, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewArtifactManager(root, config.ArtifactConfig{MaxDirectBase64Bytes: 4, Backend: "base64"})
	sendFile := NewSendFileTool(manager)
	args, _ := json.Marshal(map[string]any{"path": file})
	_, err := sendFile.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil {
		t.Fatal("expected oversized file error")
	}
}
