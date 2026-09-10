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
	arguments, _ := json.Marshal(map[string]string{"source": resource.ID})
	result, err := toolValue.Call(ctx, tool.CallRequest{Arguments: arguments})
	if err != nil {
		t.Fatalf("call send_file: %v", err)
	}
	if len(result.Outputs) != 1 || result.Outputs[0].Source.MediaID != resource.ID || result.Outputs[0].Source.Path != "" || result.Outputs[0].Name != resource.Name {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestSendFileSchemaOnlyExposesSource(t *testing.T) {
	schema := NewSendFileTool(NewFileManager(t.TempDir(), DefaultFileDeliveryConfigForTest())).Schema()
	properties, ok := schema.Function.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties missing: %#v", schema.Function.Parameters)
	}
	if _, ok := properties["source"]; !ok {
		t.Fatal("source property missing")
	}
	if _, ok := properties["media"]; ok {
		t.Fatal("media property must not be exposed")
	}
}

func DefaultFileDeliveryConfigForTest() (cfg config.FileDeliveryConfig) {
	return config.FileDeliveryConfig{MaxDirectBase64Bytes: 8 * 1024 * 1024, Backend: "base64"}
}
