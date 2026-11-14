package control

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"elbot/internal/config"
	"elbot/internal/operationresult"
)

func TestFilesSaveExactContentAndPreservePermissions(t *testing.T) {
	m, dir := testSupervisor(t, SupervisorDeps{})
	path := filepath.Join(dir, "custom.toml")
	before := append([]byte{0xef, 0xbb, 0xbf}, []byte("old\r\nline\r\n")...)
	if err := os.WriteFile(path, before, 0o640); err != nil {
		t.Fatal(err)
	}
	read, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "config", Path: "app.toml"})
	if err != nil {
		t.Fatal(err)
	}
	if read.Status != operationresult.Success || read.Data.Content != string(before) || read.Data.Revision != fileRevision(before) {
		t.Fatalf("read metadata: %+v", read)
	}
	result, err := m.WriteFile(testContext(true), WriteFileRequest{Root: "config", Path: "app.toml", Content: "new\nline\n", ExpectedRevision: read.Data.Revision})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("new\nline\n")
	if result.Status != operationresult.Success || !bytes.Equal(after, want) || result.Data.Revision != fileRevision(want) {
		t.Fatalf("after = %q", after)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("permissions: %v %v", info, err)
	}
	conflict, err := m.WriteFile(testContext(true), WriteFileRequest{Root: "config", Path: "app.toml", Content: "stale", ExpectedRevision: read.Data.Revision})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, conflict, operationresult.Conflict)
	invalid, err := m.WriteFile(testContext(true), WriteFileRequest{Root: "config", Path: "app.toml", Content: "missing revision"})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, invalid, operationresult.InvalidArguments)
	conflict, err = m.WriteFile(testContext(true), WriteFileRequest{Root: "config", Path: "app.toml", Content: "overwrite", Create: true})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, conflict, operationresult.Conflict)
}

func TestCreateAndConcurrentSave(t *testing.T) {
	m, dir := testSupervisor(t, SupervisorDeps{})
	created, err := m.WriteFile(testContext(true), WriteFileRequest{Root: "skills", Path: "agent/new/SKILL.md", Content: "original\n", Create: true})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != operationresult.Success || !created.Data.Created {
		t.Fatal("create result did not identify new file")
	}
	info, err := os.Stat(filepath.Join(dir, "skills/agent/new/SKILL.md"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("new file mode: %v %v", info, err)
	}
	start := make(chan struct{})
	type outcome struct {
		status operationresult.Status
		err    error
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for _, content := range []string{"first\n", "second\n"} {
		wg.Go(func() {
			<-start
			result, err := m.WriteFile(testContext(true), WriteFileRequest{Root: "skills", Path: "agent/new/SKILL.md", Content: content, ExpectedRevision: created.Data.Revision})
			results <- outcome{status: result.Status, err: err}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for outcome := range results {
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if outcome.status == operationresult.Success {
			success++
		} else if outcome.status == operationresult.Conflict {
			conflicts++
		} else {
			t.Fatalf("unexpected save result: %+v", outcome)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
	createdEnv, err := m.WriteFile(testContext(true), WriteFileRequest{Root: "config", Path: ".env", Content: "KEY=value\n", Create: true})
	if err != nil || createdEnv.Status != operationresult.Success {
		t.Fatalf("create .env: %+v %v", createdEnv, err)
	}
}

func TestExternalConfigAndManagedCatalog(t *testing.T) {
	dir, external := t.TempDir(), t.TempDir()
	provider := filepath.Join(external, "actual-providers.toml")
	if err := os.WriteFile(provider, []byte("provider = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := NewSupervisor(&config.Config{ConfigPath: filepath.Join(dir, "other.toml"), ProvidersConfigPath: provider}, SupervisorDeps{})
	if err != nil {
		t.Fatal(err)
	}
	read, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "config", Path: "providers.toml"})
	if err != nil || read.Status != operationresult.Success || read.Data.Content != "provider = true\n" {
		t.Fatalf("external config: %+v %v", read, err)
	}
	listed, err := m.ListFiles(testContext(true), ListFilesRequest{Root: "config"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range listed.Data.Items {
		if item.Name == "providers.toml" {
			found = item.Exists
		}
	}
	if !found {
		t.Fatal("external config missing from logical catalog")
	}
	invalid, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "config", Path: provider})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, invalid, operationresult.InvalidArguments)
	invalid, err = m.ReadFile(testContext(true), ReadFileRequest{Root: "memory", Path: "other.toml"})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, invalid, operationresult.InvalidTarget)
}

func TestFileBoundaryRejectsTraversalAndSymlinks(t *testing.T) {
	m, dir := testSupervisor(t, SupervisorDeps{})
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "skills", "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../custom.toml", "escape/secret.md"} {
		result, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "skills", Path: path})
		if err == nil && result.Status == operationresult.Success {
			t.Fatalf("read escaped through %q", path)
		}
		resultWrite, err := m.WriteFile(testContext(true), WriteFileRequest{Root: "skills", Path: path, Create: true, Content: "replace"})
		if err == nil && resultWrite.Status == operationresult.Success {
			t.Fatalf("write escaped through %q", path)
		}
	}
	// A configured child root must not become arbitrary filesystem access when replaced by a symlink.
	if err := os.Symlink(outside, filepath.Join(dir, "frontend")); err != nil {
		t.Fatal(err)
	}
	result, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "frontend", Path: "secret.md"})
	if err == nil && result.Status == operationresult.Success {
		t.Fatal("root directory symlink escaped the configuration tree")
	}
	data, err := os.ReadFile(filepath.Join(outside, "secret.md"))
	if err != nil || string(data) != "outside" {
		t.Fatalf("outside file changed: %q %v", data, err)
	}
}

func TestLogReadRangesAndReadOnly(t *testing.T) {
	logdir := t.TempDir()
	m, _ := testSupervisor(t, SupervisorDeps{LogDir: logdir})
	content := "开始娅end\n"
	if err := os.WriteFile(filepath.Join(logdir, "runtime.log"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	listed, err := m.ListFiles(testContext(true), ListFilesRequest{Root: "logs"})
	if err != nil || listed.Status != operationresult.Success || len(listed.Data.Items) != 1 || listed.Data.Items[0].Writable {
		t.Fatalf("log catalog: %+v %v", listed, err)
	}
	first, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "logs", Path: "runtime.log", Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != operationresult.Success || first.Data.Content != content[:4] || !first.Data.Truncated || first.Data.NextOffset != 4 {
		t.Fatalf("first chunk: %+v", first)
	}
	second, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "logs", Path: "runtime.log", Offset: first.Data.NextOffset, Limit: 4})
	if err != nil || second.Status != operationresult.Success || second.Data.Content != content[4:8] || second.Data.NextOffset != 8 {
		t.Fatalf("second chunk: %+v %v", second, err)
	}
	part, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "logs", Path: "runtime.log", Offset: 1, Limit: 1})
	if err != nil || part.Status != operationresult.Success || part.Data.Content != content[1:2] || part.Data.NextOffset != 2 {
		t.Fatalf("exact byte range: %+v %v", part, err)
	}
	rest, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "logs", Path: "runtime.log", Offset: second.Data.NextOffset})
	if err != nil || rest.Status != operationresult.Success || rest.Data.Content != content[8:] || rest.Data.NextOffset != int64(len(content)) || rest.Data.Truncated {
		t.Fatalf("read to end: %+v %v", rest, err)
	}
	forbidden, err := m.WriteFile(testContext(true), WriteFileRequest{Root: "logs", Path: "runtime.log", Content: "clear", ExpectedRevision: first.Data.Revision})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, forbidden, operationresult.Forbidden)
}

func TestRegularFilesSupportCallerReadRanges(t *testing.T) {
	m, _ := testSupervisor(t, SupervisorDeps{})
	part, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "config", Path: "app.toml", Offset: 1, Limit: 2})
	if err != nil || part.Status != operationresult.Success || part.Data.Content != "ni" || part.Data.Offset != 1 || part.Data.NextOffset != 3 || part.Data.Size != 8 || !part.Data.Truncated {
		t.Fatalf("partial read: %+v %v", part, err)
	}
	rest, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "config", Path: "app.toml", Offset: 3})
	if err != nil || rest.Status != operationresult.Success || rest.Data.Content != "tial\n" || rest.Data.NextOffset != 8 || rest.Data.Truncated {
		t.Fatalf("read to end: %+v %v", rest, err)
	}
}

func TestFileWriteCancellation(t *testing.T) {
	m, dir := testSupervisor(t, SupervisorDeps{})
	ctx, cancel := context.WithCancel(testContext(true))
	cancel()
	_, err := m.WriteFile(ctx, WriteFileRequest{Root: "skills", Path: "cancel/SKILL.md", Content: "x", Create: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("write cancellation error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "skills")); !os.IsNotExist(err) {
		t.Fatal("canceled request created files or directories")
	}
}

func TestLogReadOnlyCannotBeBypassedThroughAnotherRoot(t *testing.T) {
	dir := t.TempDir()
	logdir := filepath.Join(dir, "skills", "logs")
	if err := os.MkdirAll(logdir, 0o755); err != nil {
		t.Fatal(err)
	}
	logpath := filepath.Join(logdir, "runtime.log")
	content := []byte("log contents\n")
	if err := os.WriteFile(logpath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("skills/logs/runtime.log", filepath.Join(dir, "memories.toml")); err != nil {
		t.Fatal(err)
	}
	m, err := NewSupervisor(&config.Config{ConfigPath: filepath.Join(dir, "app.toml")}, SupervisorDeps{LogDir: logdir})
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []WriteFileRequest{
		{Root: "skills", Path: "logs/runtime.log", Content: "overwrite", ExpectedRevision: fileRevision(content)},
		{Root: "memory", Path: "memories.toml", Content: "overwrite", ExpectedRevision: fileRevision(content)},
	} {
		result, err := m.WriteFile(testContext(true), req)
		if err != nil {
			t.Fatal(err)
		}
		requireStatus(t, result, operationresult.Forbidden)
	}
	after, err := os.ReadFile(logpath)
	if err != nil || !bytes.Equal(after, content) {
		t.Fatalf("log changed: %q %v", after, err)
	}
}

func TestManagedFilesHaveNoTwoMiBSizeLimit(t *testing.T) {
	m, _ := testSupervisor(t, SupervisorDeps{})
	content := strings.Repeat("x", 3<<20)
	created, err := m.WriteFile(testContext(true), WriteFileRequest{Root: "skills", Path: "large/SKILL.md", Content: content, Create: true})
	if err != nil || created.Status != operationresult.Success {
		t.Fatal(err)
	}
	read, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "skills", Path: "large/SKILL.md"})
	if err != nil || read.Status != operationresult.Success {
		t.Fatal(err)
	}
	if read.Data.Content != content || read.Data.Truncated {
		t.Fatal("large file was truncated")
	}
	updated, err := m.WriteFile(testContext(true), WriteFileRequest{Root: "skills", Path: "large/SKILL.md", Content: content + "updated", ExpectedRevision: created.Data.Revision})
	if err != nil || updated.Status != operationresult.Success {
		t.Fatal(err)
	}
	if updated.Data.Created || updated.Data.Revision == created.Data.Revision {
		t.Fatalf("large update failed: %+v", updated)
	}
}

func TestLogReadCanRequestMoreThanTwoMiB(t *testing.T) {
	logdir := t.TempDir()
	m, _ := testSupervisor(t, SupervisorDeps{LogDir: logdir})
	content := strings.Repeat("x", 3<<20)
	if err := os.WriteFile(filepath.Join(logdir, "large.log"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	read, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "logs", Path: "large.log", Limit: 1<<63 - 1})
	if err != nil || read.Status != operationresult.Success {
		t.Fatal(err)
	}
	if read.Data.Content != content || read.Data.Truncated {
		t.Fatal("large log request was capped or overflowed")
	}
}
