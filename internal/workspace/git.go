package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
		data, err := os.ReadFile(abs)
		if err != nil {
			return "", err
		}
		return untrackedDiff(relative, data), nil
	}
	if _, err := gitOutput(ctx, status.Root, "rev-parse", "--verify", "HEAD"); err != nil {
		staged, err := gitOutput(ctx, status.Root, "diff", "--cached", "--", relative)
		if err != nil {
			return "", err
		}
		worktree, err := gitOutput(ctx, status.Root, "diff", "--", relative)
		return staged + worktree, err
	}
	return gitOutput(ctx, status.Root, "diff", "HEAD", "--", relative)
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

func untrackedDiff(path string, content []byte) string {
	if bytes.IndexByte(content, 0) >= 0 {
		return fmt.Sprintf("diff --git a/%s b/%s\nnew file mode 100644\nBinary files /dev/null and b/%s differ\n", path, path, path)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\nnew file mode 100644\n--- /dev/null\n+++ b/%s\n", path, path, path)
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(lines))
	if len(content) == 0 {
		return b.String()
	}
	for _, line := range lines {
		b.WriteByte('+')
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func untrackedSymlinkDiff(path, target string) string {
	return fmt.Sprintf("diff --git a/%s b/%s\nnew file mode 120000\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1 @@\n+%s\n", path, path, path, target)
}
