package builtin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"elbot/internal/config"
	"elbot/internal/media"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool"
)

func TestSendFileMediaIDBuildsLocalOutput(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()
	root := filepath.Join(t.TempDir(), "sandbox")
	files := NewFileManagerWithMedia(root, DefaultFileDeliveryConfigForTest(), store)
	resource, err := files.Media.ImportBytes(ctx, []byte("hello"), media.Input{Name: "hello.txt", MIMEType: "text/plain"})
	if err != nil {
		t.Fatalf("import media: %v", err)
	}

	toolValue := NewSendFileTool(files)
	arguments, _ := json.Marshal(map[string]string{"media": resource.ID})
	result, err := toolValue.Call(ctx, tool.CallRequest{Arguments: arguments})
	if err != nil {
		t.Fatalf("call send_file: %v", err)
	}
	if len(result.Outputs) != 1 || result.Outputs[0].Source.Path != resource.LocalPath || result.Outputs[0].Name != resource.Name {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestSendFileRejectsSourceAndMediaIDTogether(t *testing.T) {
	toolValue := NewSendFileTool(NewFileManager(t.TempDir(), DefaultFileDeliveryConfigForTest()))
	arguments, _ := json.Marshal(map[string]string{"source": "file.txt", "media": "media:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
	_, err := toolValue.Call(context.Background(), tool.CallRequest{Arguments: arguments})
	if err == nil || err.Error() != "source and media are mutually exclusive" {
		t.Fatalf("error = %v", err)
	}
}

func DefaultFileDeliveryConfigForTest() (cfg config.FileDeliveryConfig) {
	return config.FileDeliveryConfig{MaxDirectBase64Bytes: 8 * 1024 * 1024, Backend: "base64"}
}
