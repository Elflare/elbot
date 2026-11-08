package control

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"elbot/internal/config"
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
	if read.Content != string(before) || read.Revision != fileRevision(before) {
		t.Fatalf("read metadata: %+v", read)
	}
	result, err := m.WriteFile(testContext(true), WriteFileRequest{Root: "config", Path: "app.toml", Content: "new\nline\n", ExpectedRevision: read.Revision})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("new\nline\n")
	if !bytes.Equal(after, want) || result.Revision != fileRevision(want) {
		t.Fatalf("after = %q", after)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("permissions: %v %v", info, err)
	}
	_, err = m.WriteFile(testContext(true), WriteFileRequest{Root: "config", Path: "app.toml", Content: "stale", ExpectedRevision: read.Revision})
	requireCode(t, err, "file_changed")
	_, err = m.WriteFile(testContext(true), WriteFileRequest{Root: "config", Path: "app.toml", Content: "missing revision"})
	requireCode(t, err, "invalid_arguments")
	_, err = m.WriteFile(testContext(true), WriteFileRequest{Root: "config", Path: "app.toml", Content: "overwrite", Create: true})
	requireCode(t, err, "file_changed")
}

func TestCreateAndConcurrentSave(t *testing.T) {
	m, dir := testSupervisor(t, SupervisorDeps{})
	created, err := m.WriteFile(testContext(true), WriteFileRequest{Root: "skills", Path: "agent/new/SKILL.md", Content: "original\n", Create: true})
	if err != nil {
		t.Fatal(err)
	}
	if !created.Created {
		t.Fatal("create result did not identify new file")
	}
	info, err := os.Stat(filepath.Join(dir, "skills/agent/new/SKILL.md"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("new file mode: %v %v", info, err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, content := range []string{"first\n", "second\n"} {
		wg.Go(func() {
			<-start
			_, err := m.WriteFile(testContext(true), WriteFileRequest{Root: "skills", Path: "agent/new/SKILL.md", Content: content, ExpectedRevision: created.Revision})
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			requireCode(t, err, "file_changed")
			conflicts++
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
	if _, err := m.WriteFile(testContext(true), WriteFileRequest{Root: "config", Path: ".env", Content: "KEY=value\n", Create: true}); err != nil {
		t.Fatal(err)
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
	if err != nil || read.Content != "provider = true\n" {
		t.Fatalf("external config: %+v %v", read, err)
	}
	listed, err := m.ListFiles(testContext(true), ListFilesRequest{Root: "config"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range listed.Items {
		if item.Name == "providers.toml" {
			found = item.Exists
		}
	}
	if !found {
		t.Fatal("external config missing from logical catalog")
	}
	_, err = m.ReadFile(testContext(true), ReadFileRequest{Root: "config", Path: provider})
	requireCode(t, err, "invalid_arguments")
	_, err = m.ReadFile(testContext(true), ReadFileRequest{Root: "memory", Path: "other.toml"})
	requireCode(t, err, "invalid_target")
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
		if _, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "skills", Path: path}); err == nil {
			t.Fatalf("read escaped through %q", path)
		}
		if _, err := m.WriteFile(testContext(true), WriteFileRequest{Root: "skills", Path: path, Create: true, Content: "replace"}); err == nil {
			t.Fatalf("write escaped through %q", path)
		}
	}
	// A configured child root must not become arbitrary filesystem access when replaced by a symlink.
	if err := os.Symlink(outside, filepath.Join(dir, "frontend")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "frontend", Path: "secret.md"}); err == nil {
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
	if err != nil || len(listed.Items) != 1 || listed.Items[0].Writable {
		t.Fatalf("log catalog: %+v %v", listed, err)
	}
	first, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "logs", Path: "runtime.log", Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if first.Content != content[:4] || !first.Truncated || first.NextOffset != 4 {
		t.Fatalf("first chunk: %+v", first)
	}
	second, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "logs", Path: "runtime.log", Offset: first.NextOffset, Limit: 4})
	if err != nil || second.Content != content[4:8] || second.NextOffset != 8 {
		t.Fatalf("second chunk: %+v %v", second, err)
	}
	part, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "logs", Path: "runtime.log", Offset: 1, Limit: 1})
	if err != nil || part.Content != content[1:2] || part.NextOffset != 2 {
		t.Fatalf("exact byte range: %+v %v", part, err)
	}
	rest, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "logs", Path: "runtime.log", Offset: second.NextOffset})
	if err != nil || rest.Content != content[8:] || rest.NextOffset != int64(len(content)) || rest.Truncated {
		t.Fatalf("read to end: %+v %v", rest, err)
	}
	_, err = m.WriteFile(testContext(true), WriteFileRequest{Root: "logs", Path: "runtime.log", Content: "clear", ExpectedRevision: first.Revision})
	requireCode(t, err, "permission_denied")
}

func TestRegularFilesSupportCallerReadRanges(t *testing.T) {
	m, _ := testSupervisor(t, SupervisorDeps{})
	part, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "config", Path: "app.toml", Offset: 1, Limit: 2})
	if err != nil || part.Content != "ni" || part.Offset != 1 || part.NextOffset != 3 || part.Size != 8 || !part.Truncated {
		t.Fatalf("partial read: %+v %v", part, err)
	}
	rest, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "config", Path: "app.toml", Offset: 3})
	if err != nil || rest.Content != "tial\n" || rest.NextOffset != 8 || rest.Truncated {
		t.Fatalf("read to end: %+v %v", rest, err)
	}
}

func TestFileWriteCancellation(t *testing.T) {
	m, dir := testSupervisor(t, SupervisorDeps{})
	ctx, cancel := context.WithCancel(testContext(true))
	cancel()
	_, err := m.WriteFile(ctx, WriteFileRequest{Root: "skills", Path: "cancel/SKILL.md", Content: "x", Create: true})
	requireCode(t, err, "canceled")
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
		_, err := m.WriteFile(testContext(true), req)
		requireCode(t, err, "permission_denied")
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
	if err != nil {
		t.Fatal(err)
	}
	read, err := m.ReadFile(testContext(true), ReadFileRequest{Root: "skills", Path: "large/SKILL.md"})
	if err != nil {
		t.Fatal(err)
	}
	if read.Content != content || read.Truncated {
		t.Fatal("large file was truncated")
	}
	updated, err := m.WriteFile(testContext(true), WriteFileRequest{Root: "skills", Path: "large/SKILL.md", Content: content + "updated", ExpectedRevision: created.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Created || updated.Revision == created.Revision {
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
	if err != nil {
		t.Fatal(err)
	}
	if read.Content != content || read.Truncated {
		t.Fatal("large log request was capped or overflowed")
	}
}
