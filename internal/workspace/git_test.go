package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

	status, err := ReadGitStatus(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !status.IsGit || status.Root != root || len(status.Changes) != 3 {
		t.Fatalf("status = %+v", status)
	}
	byPath := map[string]GitChange{}
	for _, change := range status.Changes {
		byPath[change.Path] = change
	}
	if byPath["tracked.txt"].Status != "modified" || !byPath["tracked.txt"].Worktree || byPath["staged.txt"].Status != "added" || !byPath["staged.txt"].Staged || byPath["nested/untracked.txt"].Status != "untracked" {
		t.Fatalf("change map = %+v", byPath)
	}
	trackedDiff, err := ReadGitDiff(t.Context(), root, "tracked.txt")
	if err != nil || !strings.Contains(trackedDiff, "+worktree") {
		t.Fatalf("tracked diff = %q, %v", trackedDiff, err)
	}
	untrackedDiff, err := ReadGitDiff(t.Context(), root, "nested/untracked.txt")
	if err != nil || !strings.Contains(untrackedDiff, "+new file") {
		t.Fatalf("untracked diff = %q, %v", untrackedDiff, err)
	}
	if _, err := ReadGitDiff(t.Context(), root, "../outside"); err == nil {
		t.Fatal("outside path accepted")
	}
}

func TestReadGitStatusReturnsNonRepository(t *testing.T) {
	status, err := ReadGitStatus(t.Context(), t.TempDir())
	if err != nil || status.IsGit || len(status.Changes) != 0 {
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
