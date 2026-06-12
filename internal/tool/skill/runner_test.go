package skill

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"elbot/internal/tool"
)

func TestSafeRelativePathRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := safeRelativePath(root, "../evil.py"); err == nil {
		t.Fatal("expected path escape error")
	}
	if _, err := safeRelativePath(root, filepath.Join("scripts", "ok.py")); err != nil {
		t.Fatal(err)
	}
}

func TestGoRunnerPassesPayloadJSONToBinary(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte(`package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	var payload map[string]string
	if err := json.NewDecoder(os.Stdin).Decode(&payload); err != nil {
		fmt.Println("decode failed")
		os.Exit(1)
	}
	fmt.Printf(`+"`"+`{"content":"%s"}`+"`"+`, payload["input"])
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "helper")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binary, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, output)
	}
	catalog := NewCatalog()
	catalog.Replace([]Record{{Name: "echo", Kind: KindGo, Root: dir, BinaryPath: binary}})
	args := []byte(`{"skill_name":"echo","payload":{"input":"hello"}}`)
	result, err := NewGoRunner(catalog).Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "hello" {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestGoRunnerRejectsMissingPayload(t *testing.T) {
	_, err := parseGoRunnerArgs([]byte(`{"skill_name":"echo","input":"hello"}`))
	if err == nil || !strings.Contains(err.Error(), "payload is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestCommandErrorIncludesSkillOutput(t *testing.T) {
	err := commandError(context.Background(), "go skill", &exec.ExitError{}, "bad stdout", "bad stderr")
	text := err.Error()
	for _, want := range []string{"go skill process failed", "stdout:\nbad stdout", "stderr:\nbad stderr"} {
		if !strings.Contains(text, want) {
			t.Fatalf("error %q missing %q", text, want)
		}
	}
}

func TestResultFromStdoutHandlesTextAndJSON(t *testing.T) {
	plain, err := resultFromStdout("ok")
	if err != nil || plain.Content != "ok" || len(plain.Data) != 0 {
		t.Fatalf("plain=%#v err=%v", plain, err)
	}
	structured, err := resultFromStdout(`{"content":"done","data":{"path":"out.txt"}}`)
	if err != nil || structured.Content != "done" || len(structured.Data) != 0 {
		t.Fatalf("structured=%#v err=%v", structured, err)
	}
}
