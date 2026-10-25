package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/platform"
	workspacepath "elbot/internal/workspace"
)

func TestSendFileAssessRiskExternalPath(t *testing.T) {
	sendFile := NewSendFileTool(nil)
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte("report"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"source": path})
	assessment, err := preparedRisk(context.Background(), sendFile, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Level != toolshare.RiskHigh {
		t.Fatalf("risk = %s, want high", assessment.Level)
	}
}

func TestSendFileAssessRiskURL(t *testing.T) {
	sendFile := NewSendFileTool(nil)
	args, _ := json.Marshal(map[string]any{"source": "https://example.com/report.txt"})
	assessment, err := preparedRisk(context.Background(), sendFile, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Level != toolshare.RiskMedium {
		t.Fatalf("risk = %s, want medium", assessment.Level)
	}
}

func TestSendFileAssessRiskBackgroundAbsolutePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox")
	sendFile := NewSendFileTool(nil)
	args, _ := json.Marshal(map[string]any{"source": filepath.Join(t.TempDir(), "report.txt")})
	ctx := workspacepath.WithPathScope(contextinfo.WithBackgroundKind(context.Background(), contextinfo.BackgroundKindCron), workspacepath.PathScope{Dir: filepath.Join(root, "cron"), Restricted: true})
	_, err := preparedRisk(ctx, sendFile, toolshare.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "restricted path must be relative") {
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
	sendFile := NewSendFileTool(nil)
	args, _ := json.Marshal(map[string]any{"source": "report.txt"})
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqonebot"}}})
	ctx = workspacepath.WithPathScope(contextinfo.WithBackgroundKind(ctx, contextinfo.BackgroundKindCron), workspacepath.PathScope{Dir: cronDir, Restricted: true})
	result, _, err := callTool(ctx, sendFile, toolshare.CallRequest{Arguments: args})
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
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "report.txt"), []byte("workspace"), 0o644); err != nil {
		t.Fatal(err)
	}
	sendFile := NewSendFileTool(nil)
	args, _ := json.Marshal(map[string]any{"source": "report.txt"})
	ctx := workspacepath.WithWorkspaceStore(context.Background(), &testWorkspaceStore{dir: workspace})
	result, _, err := callTool(ctx, sendFile, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs[0].Source.Path; got != filepath.Join(workspace, "report.txt") {
		t.Fatalf("sent path = %q", got)
	}
}

func TestSendFileSendsExternalFileDirectly(t *testing.T) {
	source := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(source, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	sendFile := NewSendFileTool(nil)
	args, _ := json.Marshal(map[string]any{"source": source})
	result, _, err := callTool(context.Background(), sendFile, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	sentPath := result.Outputs[0].Source.Path
	if sentPath != source {
		t.Fatalf("sent path = %q, want source", sentPath)
	}
}

func TestSendFileAcceptsFileURI(t *testing.T) {
	source := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(source, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	sendFile := NewSendFileTool(nil)
	args, _ := json.Marshal(map[string]any{"source": fileURI(source)})
	result, _, err := callTool(context.Background(), sendFile, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs[0].Source.Path; got != source {
		t.Fatalf("path = %q, want %q", got, source)
	}
}

func TestSendFileSendsURLFile(t *testing.T) {
	sendFile := NewSendFileTool(nil)
	args, _ := json.Marshal(map[string]any{"source": "https://example.com/report.txt"})
	result, _, err := callTool(context.Background(), sendFile, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	out := result.Outputs[0]
	if out.Kind != delivery.KindFile || out.Source.URL != "https://example.com/report.txt" || out.Source.Path != "" {
		t.Fatalf("output = %#v", out)
	}
	if out.Name != "report.txt" {
		t.Fatalf("name = %q", out.Name)
	}
}

func TestSendFileSendsURLImage(t *testing.T) {
	sendFile := NewSendFileTool(nil)
	args, _ := json.Marshal(map[string]any{"source": "https://example.com/images/cat.png?size=large"})
	result, _, err := callTool(context.Background(), sendFile, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	out := result.Outputs[0]
	if out.Kind != delivery.KindImage || out.Source.URL != "https://example.com/images/cat.png?size=large" {
		t.Fatalf("output = %#v", out)
	}
	if out.Source.MIMEType != "image/png" {
		t.Fatalf("mime = %q, want image/png", out.Source.MIMEType)
	}
}

func TestSendFileMIMETypeOverridesURLName(t *testing.T) {
	sendFile := NewSendFileTool(nil)
	args, _ := json.Marshal(map[string]any{"source": "https://example.com/download", "name": "download", "mime_type": "image/webp"})
	result, _, err := callTool(context.Background(), sendFile, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs[0].Kind != delivery.KindImage {
		t.Fatalf("output = %#v", result.Outputs[0])
	}
}

func TestSendFileURLPathExtensionOverridesDisplayName(t *testing.T) {
	sendFile := NewSendFileTool(nil)
	args, _ := json.Marshal(map[string]any{"source": "https://example.com/cat.png", "name": "cat.txt"})
	result, _, err := callTool(context.Background(), sendFile, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs[0].Kind != delivery.KindImage {
		t.Fatalf("output = %#v", result.Outputs[0])
	}
}

func TestSendFileHTTPFolderIsLocalPath(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "http"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "http", "cat.png"), []byte("not really png"), 0o644); err != nil {
		t.Fatal(err)
	}
	sendFile := NewSendFileTool(nil)
	args, _ := json.Marshal(map[string]any{"source": "http/cat.png"})
	ctx := workspacepath.WithWorkspaceStore(context.Background(), &testWorkspaceStore{dir: workspace})
	result, _, err := callTool(ctx, sendFile, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	out := result.Outputs[0]
	if out.Source.URL != "" || out.Source.Path != filepath.Join(workspace, "http", "cat.png") {
		t.Fatalf("output = %#v", out)
	}
}

func TestSendFileSendsLocalImage(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "cat.png"), []byte("not really png"), 0o644); err != nil {
		t.Fatal(err)
	}
	sendFile := NewSendFileTool(nil)
	args, _ := json.Marshal(map[string]any{"source": "cat.png"})
	ctx := workspacepath.WithWorkspaceStore(context.Background(), &testWorkspaceStore{dir: workspace})
	result, _, err := callTool(ctx, sendFile, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	out := result.Outputs[0]
	if out.Kind != delivery.KindImage || out.Source.Path != filepath.Join(workspace, "cat.png") {
		t.Fatalf("output = %#v", out)
	}
	if out.Source.MIMEType != "image/png" {
		t.Fatalf("mime = %q, want image/png", out.Source.MIMEType)
	}
}

func TestSendFileRejectsMissingSource(t *testing.T) {
	sendFile := NewSendFileTool(nil)
	_, _, err := callTool(context.Background(), sendFile, toolshare.CallRequest{Arguments: []byte(`{}`)})
	if err == nil || !strings.Contains(err.Error(), "source is required") {
		t.Fatalf("expected source error, got %v", err)
	}
}

func TestSendFileBackgroundRejectsAbsolutePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox")
	file := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	sendFile := NewSendFileTool(nil)
	args, _ := json.Marshal(map[string]any{"source": file})
	ctx := workspacepath.WithPathScope(contextinfo.WithBackgroundKind(context.Background(), contextinfo.BackgroundKindCron), workspacepath.PathScope{Dir: filepath.Join(root, "cron"), Restricted: true})
	_, _, err := callTool(ctx, sendFile, toolshare.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "restricted path must be relative") {
		t.Fatalf("expected background absolute path rejection, got %v", err)
	}
}

func fileURI(path string) string {
	path = filepath.ToSlash(path)
	if runtime.GOOS == "windows" {
		return "file:///" + url.PathEscape(path[:2]) + path[2:]
	}
	return "file://" + path
}
