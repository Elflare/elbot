package workspace

import (
	"bytes"
	"context"
	"elbot/internal/operationresult"
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

func ReadGitStatus(ctx context.Context, path string) (operationresult.Result[GitStatus], error) {
	checked, err := ValidateWorkspaceDir(path)
	if err != nil {
		return operationresult.Result[GitStatus]{}, err
	}
	if checked.Status != operationresult.Success {
		return operationresult.Result[GitStatus]{Status: checked.Status, Message: checked.Message}, nil
	}
	path = checked.Data
	root, err := gitOutput(ctx, path, "rev-parse", "--show-toplevel")
	root = strings.TrimSpace(root)
	var output []byte
	if err == nil {
		output, err = gitBytes(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--renames")
	}
	if err != nil {
		if err := ctx.Err(); err != nil {
			return operationresult.Result[GitStatus]{}, err
		}
		return operationresult.Result[GitStatus]{Status: operationresult.Success, Data: GitStatus{Path: path, Changes: []GitChange{}}}, nil
	}
	changes := parseGitStatus(output)
	return operationresult.Result[GitStatus]{Status: operationresult.Success, Data: GitStatus{IsGit: true, Path: path, Root: root, Changes: changes}}, nil
}

func ReadGitDiff(ctx context.Context, path, relative string) (operationresult.Result[string], error) {
	loaded, err := ReadGitStatus(ctx, path)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if loaded.Status != operationresult.Success {
		return operationresult.Result[string]{Status: loaded.Status, Message: loaded.Message}, nil
	}
	status := loaded.Data
	if !status.IsGit {
		return operationresult.Result[string]{Status: operationresult.InvalidTarget, Message: "非 Git 仓库或 Git 配置无效"}, nil
	}
	relative = filepath.Clean(filepath.FromSlash(strings.TrimSpace(relative)))
	if !filepath.IsLocal(relative) || relative == "." {
		return operationresult.Result[string]{Status: operationresult.InvalidArguments, Message: "invalid Git path"}, nil
	}
	var change *GitChange
	for i := range status.Changes {
		if filepath.Clean(filepath.FromSlash(status.Changes[i].Path)) == relative {
			change = &status.Changes[i]
			break
		}
	}
	if change == nil {
		return operationresult.Result[string]{Status: operationresult.NotFound, Message: "Git path has no changes"}, nil
	}
	var staged string
	args := []string{"diff", "HEAD", "--", relative}
	if change.Status == "untracked" {
		abs := filepath.Join(status.Root, relative)
		info, err := os.Lstat(abs)
		if err != nil {
			return operationresult.Result[string]{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(abs)
			if err != nil {
				return operationresult.Result[string]{}, err
			}
			return operationresult.Result[string]{Status: operationresult.Success, Data: untrackedSymlinkDiff(relative, target)}, nil
		}
		if !info.Mode().IsRegular() {
			return operationresult.Result[string]{Status: operationresult.InvalidTarget, Message: "Git path is not a regular file"}, nil
		}
		args = []string{"diff", "--no-index", "--src-prefix=a/", "--dst-prefix=b/", "--", "/dev/null", relative}
	} else {
		head, err := gitOutput(ctx, status.Root, "status", "--porcelain=v2", "--branch", "--untracked-files=no")
		if err != nil {
			return operationresult.Result[string]{}, err
		}
		if slices.Contains(strings.Split(head, "\n"), "# branch.oid (initial)") {
			var exceeded bool
			staged, exceeded, err = gitDiffOutput(ctx, status.Root, "diff", "--cached", "--", relative)
			if err != nil {
				return operationresult.Result[string]{}, err
			}
			if exceeded {
				return operationresult.Result[string]{Status: operationresult.Success, Data: largeGitDiffOutput}, nil
			}
			args = []string{"diff", "--", relative}
		}
	}
	diff, exceeded, err := gitDiffOutput(ctx, status.Root, args...)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if exceeded || len(staged)+len(diff) > maxGitDiffOutput {
		return operationresult.Result[string]{Status: operationresult.Success, Data: largeGitDiffOutput}, nil
	}
	return operationresult.Result[string]{Status: operationresult.Success, Data: staged + diff}, nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	data, err := gitBytes(ctx, dir, args...)
	return string(data), err
}

func gitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
}

func gitBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := gitCommand(ctx, dir, args...)
	data, err := cmd.Output()
	if err == nil {
		return data, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
	}
	return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
}

func gitDiffOutput(ctx context.Context, dir string, args ...string) (text string, exceeded bool, err error) {
	stdout := &boundedBuffer{limit: maxGitDiffOutput}
	var stderr bytes.Buffer
	cmd := gitCommand(ctx, dir, args...)
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
		if stdout.exceeded {
			return "", true, nil
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && slices.Contains(args, "--no-index") {
			return stdout.String(), false, nil
		}
		return "", false, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), false, nil
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

func parseGitStatus(data []byte) []GitChange {
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
