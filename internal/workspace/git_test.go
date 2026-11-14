package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"elbot/internal/operationresult"
)

func TestGitStatusAndDiffIncludeTrackedAndUntrackedChanges(t *testing.T) {
	root := t.TempDir()
	gitTestCommand(t, root, "init", "-q")
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("tracked.txt", "base\n")
	gitTestCommand(t, root, "add", "tracked.txt")
	gitTestCommand(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-q", "-m", "initial")
	write("tracked.txt", "base\nworktree\n")
	write("staged.txt", "staged\n")
	gitTestCommand(t, root, "add", "staged.txt")
	write("nested/untracked.txt", "new file\n")
	write("empty.txt", "")

	status, err := ReadGitStatus(t.Context(), root)
	if err != nil || status.Status != operationresult.Success {
		t.Fatalf("status: %+v %v", status, err)
	}
	if !status.Data.IsGit || status.Data.Root != root || len(status.Data.Changes) != 4 {
		t.Fatalf("status = %+v", status)
	}
	byPath := map[string]GitChange{}
	for _, change := range status.Data.Changes {
		byPath[change.Path] = change
	}
	if byPath["tracked.txt"].Status != "modified" || !byPath["tracked.txt"].Worktree || byPath["staged.txt"].Status != "added" || !byPath["staged.txt"].Staged || byPath["nested/untracked.txt"].Status != "untracked" {
		t.Fatalf("change map = %+v", byPath)
	}
	trackedDiff, err := ReadGitDiff(t.Context(), root, "tracked.txt")
	if err != nil || trackedDiff.Status != operationresult.Success || !strings.Contains(trackedDiff.Data, "+worktree") {
		t.Fatalf("tracked diff = %q, %v", trackedDiff, err)
	}
	untrackedDiff, err := ReadGitDiff(t.Context(), root, "nested/untracked.txt")
	if err != nil || untrackedDiff.Status != operationresult.Success || !strings.Contains(untrackedDiff.Data, "+new file") {
		t.Fatalf("untracked diff = %q, %v", untrackedDiff, err)
	}
	emptyDiff, err := ReadGitDiff(t.Context(), root, "empty.txt")
	if err != nil || emptyDiff.Status != operationresult.Success || !strings.Contains(emptyDiff.Data, "new file mode 100644") || strings.Contains(emptyDiff.Data, "@@") {
		t.Fatalf("empty file diff = %q, %v", emptyDiff, err)
	}
	if result, err := ReadGitDiff(t.Context(), root, "../outside"); err != nil || result.Status != operationresult.InvalidArguments {
		t.Fatalf("outside path: %+v %v", result, err)
	}
}

func TestReadGitDiffLimitsOutput(t *testing.T) {
	root := t.TempDir()
	gitTestCommand(t, root, "init", "-q")
	content := strings.Repeat("x", 2*maxGitDiffOutput)
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	diff, err := ReadGitDiff(t.Context(), root, "large.txt")
	if err != nil || diff.Status != operationresult.Success || diff.Data != largeGitDiffOutput {
		t.Fatalf("diff=%q err=%v", diff, err)
	}
}

func TestReadGitStatusReturnsNonRepository(t *testing.T) {
	t.Setenv("LANG", "zh_CN.UTF-8")
	t.Setenv("LANGUAGE", "zh_CN:zh")
	t.Setenv("LC_ALL", "")
	status, err := ReadGitStatus(t.Context(), t.TempDir())
	if err != nil || status.Status != operationresult.Success || status.Data.IsGit || len(status.Data.Changes) != 0 {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

func gitTestCommand(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.CommandContext(context.Background(), "git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s (%v)", args, output, err)
	}
}

func TestGitDiffCancellationAfterOutputLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX helper process")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nprintf '%070000d' 0\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	diff, err := gitDiffOutput(ctx, dir, "diff")
	if !errors.Is(err, context.DeadlineExceeded) || diff != "" {
		t.Fatalf("canceled diff=%q err=%v", diff, err)
	}
}
