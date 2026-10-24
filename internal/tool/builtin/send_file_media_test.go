package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"elbot/internal/media"
	"elbot/internal/storage/sqlite"
	
)

func TestSendFileMediaIDBuildsLocalOutput(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()
	root := filepath.Join(t.TempDir(), "sandbox")
	files := NewFileManager(root)
	files.Media = media.NewManager(store, root, &media.LocalBackend{Root: root})
	resource, err := files.Media.ImportBytes(ctx, []byte("hello"), media.Input{Name: "hello.txt", MIMEType: "text/plain"})
	if err != nil {
		t.Fatalf("import media: %v", err)
	}

	toolValue := NewSendFileTool(files)
	arguments, _ := json.Marshal(map[string]string{"source": resource.ID})
	result, _, err := toolValue.Call(ctx, toolshare.CallRequest{Arguments: arguments})
	if err != nil {
		t.Fatalf("call send_file: %v", err)
	}
	if len(result.Outputs) != 1 || result.Outputs[0].Source.MediaID != resource.ID || result.Outputs[0].Source.Path != "" || result.Outputs[0].Name != resource.Name {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestSendFileSchemaExposesSource(t *testing.T) {
	schema := NewSendFileTool(NewFileManager(t.TempDir())).Schema()
	properties, ok := schema.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties missing: %#v", schema.Parameters)
	}
	if _, ok := properties["source"]; !ok {
		t.Fatal("source property missing")
	}
}
