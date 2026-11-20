// 枚举目录文件并搜索文本内容。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"bufio"

	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"elbot/internal/fileops"
	"elbot/internal/operationresult"
	"elbot/internal/processenv"
)

type directorySearchMatch struct {
	Path   string
	Line   int
	Column int
}

func readFileDirectorySearch(ctx context.Context, root string, args readFileArgs, warnings []string) (operationresult.Result[*toolshare.Result], error) {
	query := args.Query
	if query == "" {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidArguments, Message: "query is required when mode is grep"}, nil
	}
	found, total, grepWarnings, err := findDirectoryGrepMatches(ctx, root, query, args.Index, args.Glob)
	if err != nil {
		return operationresult.Result[*toolshare.Result]{}, err
	}
	if found.Status != operationresult.Success {
		return operationresult.Result[*toolshare.Result]{Status: found.Status, Message: found.Message}, nil
	}
	warnings = append(warnings, grepWarnings...)
	return formatDirectorySearchMatches(root, query, found.Data, total, args.ContextLines, args.Index, warnings)
}

func findDirectoryGrepMatches(ctx context.Context, root, query string, index readFileIndex, glob []string) (operationresult.Result[[]directorySearchMatch], int, []string, error) {
	pattern, err := regexp.Compile(query)
	if err != nil {
		return operationresult.Result[[]directorySearchMatch]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("invalid grep regex: %v", err)}, 0, nil, nil
	}
	rg, err := exec.LookPath("rg")
	if errors.Is(err, exec.ErrNotFound) {
		return operationresult.Result[[]directorySearchMatch]{Status: operationresult.Unavailable, Message: "directory grep requires ripgrep (rg), but rg was not found in PATH. Ask the user to install ripgrep and make `rg` available in PATH"}, 0, nil, nil
	}
	if err != nil {
		return operationresult.Result[[]directorySearchMatch]{}, 0, nil, err
	}
	files, warnings, err := directoryGrepFiles(ctx, rg, root, glob)
	if err != nil {
		return operationresult.Result[[]directorySearchMatch]{}, 0, nil, err
	}
	if files.Status != operationresult.Success {
		return operationresult.Result[[]directorySearchMatch]{Status: files.Status, Message: files.Message}, 0, nil, nil
	}
	sort.Slice(files.Data, func(i, j int) bool { return filepath.ToSlash(files.Data[i]) < filepath.ToSlash(files.Data[j]) })
	var matches []directorySearchMatch
	total, err := scanDirectoryGrepBatch(ctx, root, pattern, files.Data, index, &matches)
	if err != nil {
		return operationresult.Result[[]directorySearchMatch]{}, 0, nil, err
	}
	if total.Status != operationresult.Success {
		return operationresult.Result[[]directorySearchMatch]{Status: total.Status, Message: total.Message}, 0, nil, nil
	}
	return operationresult.Result[[]directorySearchMatch]{Status: operationresult.Success, Data: matches}, total.Data, warnings, nil
}

// directoryGrepFiles uses ripgrep's ignore rules for both search and warnings.
func directoryGrepFiles(ctx context.Context, rg, root string, glob []string) (operationresult.Result[[]string], []string, error) {
	args := []string{"--files", "--null", "--no-messages"}
	for _, pattern := range glob {
		args = append(args, "--glob", pattern)
	}
	args = append(args, "--glob", "!**/.git/**", "--glob", "!**/node_modules/**", "--glob", "!**/vendor/**", "--", root)
	cmd := exec.CommandContext(ctx, rg, args...)
	processenv.ConfigureCommand(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return operationresult.Result[[]string]{}, nil, err
	}
	defer stdout.Close()
	if err := cmd.Start(); err != nil {
		return operationresult.Result[[]string]{}, nil, fmt.Errorf("enumerate ripgrep files: %w", err)
	}
	files := []string{}
	large := []struct {
		Path string
		Size int64
	}{}
	count := 0
	reader := bufio.NewReader(stdout)
	var readErr error
	for {
		path, err := reader.ReadString(0)
		if err == io.EOF && path == "" {
			break
		}
		if err != nil {
			readErr = err
			break
		}
		path = strings.TrimSuffix(path, "\x00")
		info, err := os.Stat(path)
		if err != nil {
			readErr = err
			break
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if info.Size() <= fileToolMaxBytes {
			files = append(files, path)
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			readErr = err
			break
		}
		count++
		large = append(large, struct {
			Path string
			Size int64
		}{filepath.ToSlash(rel), info.Size()})
		sort.Slice(large, func(i, j int) bool { return large[i].Path < large[j].Path })
		if len(large) > 5 {
			large = large[:5]
		}
	}
	if readErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return operationresult.Result[[]string]{}, nil, ctx.Err()
	}
	if readErr != nil {
		return operationresult.Result[[]string]{}, nil, fmt.Errorf("read ripgrep files: %w", readErr)
	}
	if waitErr != nil {
		var exitError *exec.ExitError
		if !errors.As(waitErr, &exitError) || exitError.ExitCode() < 0 {
			return operationresult.Result[[]string]{}, nil, fmt.Errorf("enumerate ripgrep files: %w", waitErr)
		}
		if exitError.ExitCode() != 1 {
			return operationresult.Result[[]string]{Status: operationresult.Failed, Message: fmt.Sprintf("enumerate ripgrep files: %v", waitErr)}, nil, nil
		}
	}
	if count == 0 {
		return operationresult.Result[[]string]{Status: operationresult.Success, Data: files}, nil, nil
	}
	preview := make([]string, 0, len(large))
	for _, file := range large {
		preview = append(preview, fmt.Sprintf("%s (%d bytes)", file.Path, file.Size))
	}
	extra := ""
	if count > len(large) {
		extra = fmt.Sprintf("; %d more", count-len(large))
	}
	return operationresult.Result[[]string]{Status: operationresult.Success, Data: files}, []string{fmt.Sprintf("grep skipped %d large files: %s%s", count, strings.Join(preview, ", "), extra)}, nil
}

func scanDirectoryGrepBatch(ctx context.Context, root string, query *regexp.Regexp, files []string, index readFileIndex, matches *[]directorySearchMatch) (operationresult.Result[int], error) {
	total := 0
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return operationresult.Result[int]{}, err
		}
		file, err := os.Open(path)
		if err != nil {
			return operationresult.Result[int]{}, fmt.Errorf("open grep file %s: %w", path, err)
		}
		count, err := func() (operationresult.Result[int], error) {
			defer file.Close()
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return operationresult.Result[int]{}, err
			}
			rel = filepath.ToSlash(rel)
			reader := bufio.NewReader(io.LimitReader(file, fileToolMaxBytes+1))
			count, lineNumber, size := 0, 0, int64(0)
			// Delay publishing candidates until the file is known to be text:
			// a NUL anywhere discards even matches preceding it.
			candidates := []directorySearchMatch{}
			for {
				if err := ctx.Err(); err != nil {
					return operationresult.Result[int]{}, err
				}
				text, err := reader.ReadString('\n')
				if err != nil && err != io.EOF {
					return operationresult.Result[int]{}, fmt.Errorf("read grep file %s: %w", path, err)
				}
				if text == "" && err == io.EOF {
					break
				}
				size += int64(len(text))
				if size > fileToolMaxBytes {
					return operationresult.Result[int]{Status: operationresult.Rejected, Message: fmt.Sprintf("file too large: %s exceeds %d bytes", path, fileToolMaxBytes)}, nil
				}
				if strings.IndexByte(text, 0) >= 0 {
					return operationresult.Result[int]{Status: operationresult.Success}, nil
				}
				if lineNumber == 0 {
					text = strings.TrimPrefix(text, "\ufeff")
				}
				// 页后首条已命中，只需确认文件剩余部分不含 NUL。
				if total+count > index.end {
					continue
				}
				for _, line := range fileops.SplitLines(text) {
					lineNumber++
					location := query.FindStringIndex(line)
					if location == nil {
						continue
					}
					count++
					if total+count > index.end {
						break
					}
					if total+count >= index.start {
						candidates = append(candidates, directorySearchMatch{Path: rel, Line: lineNumber, Column: location[0] + 1})
					}
				}
			}
			*matches = append(*matches, candidates...)
			return operationresult.Result[int]{Status: operationresult.Success, Data: count}, nil
		}()
		if err != nil {
			return operationresult.Result[int]{}, err
		}
		if count.Status != operationresult.Success {
			return count, nil
		}
		total += count.Data
		if total > index.end {
			break
		}
	}
	return operationresult.Result[int]{Status: operationresult.Success, Data: total}, nil
}

func formatDirectorySearchMatches(root, query string, matches []directorySearchMatch, total, contextLines int, index readFileIndex, warnings []string) (operationresult.Result[*toolshare.Result], error) {
	if index.outOfRange(total) {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("index %d is out of range; found %d matches", index.start, total)}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s | grep: %q | matches: %s\n", root, query, index.summary(total, false))
	if index.single {
		return formatDirectorySearchSelection(&b, root, matches[0], contextLines, warnings)
	}
	for i, match := range matches {
		fmt.Fprintf(&b, "%d. %s:%d:%d [grep] \n", i+index.start, match.Path, match.Line, match.Column)
	}
	return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Content: b.String(), Warnings: warnings}}, nil
}

func formatDirectorySearchSelection(b *strings.Builder, root string, match directorySearchMatch, contextLines int, warnings []string) (operationresult.Result[*toolshare.Result], error) {
	path := filepath.Join(root, filepath.FromSlash(match.Path))
	loaded, err := fileops.ReadFileWithLimit(path, "", fileToolMaxBytes)
	if err != nil {
		return operationresult.Result[*toolshare.Result]{}, err
	}
	if loaded.Status != operationresult.Success {
		return operationresult.Result[*toolshare.Result]{Status: loaded.Status, Message: loaded.Message}, nil
	}
	file := loaded.Data
	lines := fileops.SplitLines(file.Text)
	if match.Line < 1 || match.Line > len(lines) {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Conflict, Message: "file changed after directory search"}, nil
	}
	if int64(len(file.Bytes)) > fileToolDetailedMaxBytes {
		warnings = append(warnings, fmt.Sprintf("large file: %d bytes; ast mode is disabled", len(file.Bytes)))
	}
	start, end := max(1, match.Line-contextLines), min(len(lines), match.Line+contextLines)
	fmt.Fprintf(b, "match: %s:%d-%d [grep] \n", match.Path, match.Line, match.Line)
	width := len(fmt.Sprintf("%d", len(lines)))
	for line := start; line <= end; line++ {
		fmt.Fprintf(b, "  %*d | %s\n", width, line, lines[line-1])
	}
	return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Content: b.String(), Warnings: warnings}}, nil
}
