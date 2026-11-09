package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

const (
	maxGitDiffOutput = 64 << 10
)

const largeGitDiffOutput = "[diff omitted: output exceeds 64 KiB]"

type GitChange struct {
	Path     string `json:"path"`
	Status   string `json:"status"`
	Staged   bool   `json:"staged"`
	Worktree bool   `json:"worktree"`
}

type GitStatus struct {
	IsGit   bool        `json:"is_git"`
	Path    string      `json:"path"`
	Root    string      `json:"root,omitempty"`
	Changes []GitChange `json:"changes"`
}

func ReadGitStatus(ctx context.Context, path string) (GitStatus, error) {
	path, err := ValidateWorkspaceDir(path)
	if err != nil {
		return GitStatus{}, err
	}
	root, err := gitOutput(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		if errors.Is(err, errNotGit) {
			return GitStatus{Path: path, Changes: []GitChange{}}, nil
		}
		return GitStatus{}, err
	}
	root = strings.TrimSpace(root)
	output, err := gitBytes(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--renames")
	if err != nil {
		return GitStatus{}, err
	}
	changes := parseGitStatus(output, root)
	return GitStatus{IsGit: true, Path: path, Root: root, Changes: changes}, nil
}

func ReadGitDiff(ctx context.Context, path, relative string) (string, error) {
	status, err := ReadGitStatus(ctx, path)
	if err != nil {
		return "", err
	}
	if !status.IsGit {
		return "", fmt.Errorf("workspace is not a Git repository")
	}
	relative = filepath.Clean(filepath.FromSlash(strings.TrimSpace(relative)))
	if !filepath.IsLocal(relative) || relative == "." {
		return "", fmt.Errorf("invalid Git path")
	}
	var change *GitChange
	for i := range status.Changes {
		if filepath.Clean(filepath.FromSlash(status.Changes[i].Path)) == relative {
			change = &status.Changes[i]
			break
		}
	}
	if change == nil {
		return "", fmt.Errorf("Git path has no changes")
	}
	if change.Status == "untracked" {
		abs := filepath.Join(status.Root, relative)
		info, err := os.Lstat(abs)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(abs)
			if err != nil {
				return "", err
			}
			return untrackedSymlinkDiff(relative, target), nil
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("Git path is not a regular file")
		}
		return gitDiffOutput(ctx, status.Root, "diff", "--no-index", "--src-prefix=a/", "--dst-prefix=b/", "--", "/dev/null", relative)
	}
	if _, err := gitOutput(ctx, status.Root, "rev-parse", "--verify", "HEAD"); err != nil {
		staged, err := gitDiffOutput(ctx, status.Root, "diff", "--cached", "--", relative)
		if err != nil {
			return "", err
		}
		if staged == largeGitDiffOutput {
			return staged, nil
		}
		worktree, err := gitDiffOutput(ctx, status.Root, "diff", "--", relative)
		if err != nil || worktree == largeGitDiffOutput {
			return worktree, err
		}
		if len(staged)+len(worktree) > maxGitDiffOutput {
			return largeGitDiffOutput, nil
		}
		return staged + worktree, nil
	}
	return gitDiffOutput(ctx, status.Root, "diff", "HEAD", "--", relative)
}

var errNotGit = errors.New("not a Git repository")

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	data, err := gitBytes(ctx, dir, args...)
	return string(data), err
}

func gitBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	data, err := cmd.Output()
	if err == nil {
		return data, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && strings.Contains(string(exit.Stderr), "not a git repository") {
		return nil, errNotGit
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
}

func gitDiffOutput(ctx context.Context, dir string, args ...string) (string, error) {
	stdout := &boundedBuffer{limit: maxGitDiffOutput}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stdout.exceeded {
			return largeGitDiffOutput, nil
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && slices.Contains(args, "--no-index") {
			return stdout.String(), nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.exceeded = true
		return 0, fmt.Errorf("output limit exceeded")
	}
	if len(data) > remaining {
		_, _ = b.buffer.Write(data[:remaining])
		b.exceeded = true
		return remaining, fmt.Errorf("output limit exceeded")
	}
	return b.buffer.Write(data)
}

func (b *boundedBuffer) String() string { return b.buffer.String() }

func parseGitStatus(data []byte, root string) []GitChange {
	changes := []GitChange{}
	for len(data) > 0 {
		end := bytes.IndexByte(data, 0)
		if end < 0 {
			break
		}
		record := string(data[:end])
		data = data[end+1:]
		if len(record) < 4 {
			continue
		}
		x, y := record[0], record[1]
		name := record[3:]
		status := gitChangeStatus(x, y)
		if status == "renamed" && len(data) > 0 {
			oldEnd := bytes.IndexByte(data, 0)
			if oldEnd >= 0 {
				data = data[oldEnd+1:]
			}
		}
		changes = append(changes, GitChange{Path: filepath.ToSlash(name), Status: status, Staged: x != ' ' && x != '?', Worktree: y != ' ' && y != '?'})
	}
	return changes
}

func gitChangeStatus(x, y byte) string {
	switch {
	case x == '?' && y == '?':
		return "untracked"
	case x == 'R' || y == 'R':
		return "renamed"
	case x == 'D' || y == 'D':
		return "deleted"
	case x == 'A' || y == 'A':
		return "added"
	default:
		return "modified"
	}
}

func untrackedSymlinkDiff(path, target string) string {
	return fmt.Sprintf("diff --git a/%s b/%s\nnew file mode 120000\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1 @@\n+%s\n", path, path, path, target)
}
