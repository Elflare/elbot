// 枚举和搜索目录内容，维护候选目录及相关缓存。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"bufio"
	"container/heap"
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
	"sync"
	"time"

	"elbot/internal/fileops"
	"elbot/internal/operationresult"
	"elbot/internal/processenv"
)

type directorySearchMatch struct {
	Path    string
	Line    int
	Column  int
	EndLine int
	Kind    string
	Label   string
}

const (
	directoryASTCacheTTL     = time.Minute
	directoryASTCacheMaxSize = 64
)

type directoryASTCacheKey struct {
	Root  string
	Mode  string
	Query string
}

type directoryASTFileState struct {
	Path    string
	Size    int64
	ModTime int64
}

func directoryLargeASTWarnings(files []directoryASTFileState, mode string) []string {
	const previewLimit = 5
	large := make([]string, 0, previewLimit)
	count := 0
	for _, file := range files {
		if file.Size <= fileToolDetailedMaxBytes {
			continue
		}
		count++
		if len(large) < previewLimit {
			large = append(large, fmt.Sprintf("%s (%d bytes)", file.Path, file.Size))
		}
	}
	if count == 0 {
		return nil
	}
	extra := ""
	if count > len(large) {
		extra = fmt.Sprintf("; %d more", count-len(large))
	}
	return []string{fmt.Sprintf("%s skipped %d large files: %s%s", mode, count, strings.Join(large, ", "), extra)}
}

type directoryASTCacheEntry struct {
	Matches   []directorySearchMatch
	Files     []directoryASTFileState
	ExpiresAt time.Time
	UsedAt    time.Time
}

type directoryASTCache struct {
	mu      sync.Mutex
	entries map[directoryASTCacheKey]directoryASTCacheEntry
}

func newDirectoryASTCache() *directoryASTCache {
	return &directoryASTCache{entries: make(map[directoryASTCacheKey]directoryASTCacheEntry)}
}

func (c *directoryASTCache) load(root, mode, query string) ([]directorySearchMatch, []string, bool) {
	if c == nil {
		return nil, nil, false
	}
	key := directoryASTCacheKey{Root: root, Mode: mode, Query: query}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.ExpiresAt) {
		delete(c.entries, key)
		return nil, nil, false
	}
	if !directoryASTFilesMatch(root, entry.Files) {
		delete(c.entries, key)
		return nil, nil, false
	}
	entry.UsedAt = time.Now()
	c.entries[key] = entry
	return append([]directorySearchMatch(nil), entry.Matches...), directoryLargeASTWarnings(entry.Files, mode), true
}

func (c *directoryASTCache) store(root, mode, query string, matches []directorySearchMatch, files []directoryASTFileState) {
	if c == nil {
		return
	}
	key := directoryASTCacheKey{Root: root, Mode: mode, Query: query}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists {
		for len(c.entries) >= directoryASTCacheMaxSize {
			var oldestKey directoryASTCacheKey
			var oldestTime time.Time
			for candidateKey, entry := range c.entries {
				if oldestTime.IsZero() || entry.UsedAt.Before(oldestTime) {
					oldestKey, oldestTime = candidateKey, entry.UsedAt
				}
			}
			delete(c.entries, oldestKey)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	c.entries[key] = directoryASTCacheEntry{Matches: append([]directorySearchMatch(nil), matches...), Files: append([]directoryASTFileState(nil), files...), ExpiresAt: now.Add(directoryASTCacheTTL), UsedAt: now}
}

func readFileDirectorySearch(ctx context.Context, root, mode string, args readFileArgs, warnings []string, astCache *directoryASTCache) (operationresult.Result[*toolshare.Result], error) {
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("query is required when mode is %s", mode)}, nil
	}
	var matches []directorySearchMatch
	total := 0
	var err error
	switch mode {
	case readFileModeGrep:
		keep := fileops.NormalizeMaxMatches(args.MaxMatches)
		if args.Index > 0 {
			keep = args.Index
		}
		found, count, grepWarnings, err := findDirectoryGrepMatches(ctx, root, query, keep, args.Glob)
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err
		}
		if found.Status != operationresult.Success {
			return operationresult.Result[*toolshare.Result]{Status: found.Status, Message: found.Message}, nil
		}
		matches, total = found.Data, count
		warnings = append(warnings, grepWarnings...)
	case readFileModeAST, readFileModeASTFunction:
		if cached, cachedWarnings, ok := astCache.load(root, mode, query); ok {
			matches = cached
			warnings = append(warnings, cachedWarnings...)
		} else {
			var files []directoryASTFileState
			matches, files, err = findDirectoryASTMatches(root, query, mode)
			if err == nil {
				astCache.store(root, mode, query, matches, files)
				warnings = append(warnings, directoryLargeASTWarnings(files, mode)...)
			}
		}
	}
	if err != nil {
		return operationresult.Result[*toolshare.Result]{}, err
	}
	if mode != readFileModeGrep {
		total = len(matches)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Path == matches[j].Path {
			return matches[i].Line < matches[j].Line
		}
		return matches[i].Path < matches[j].Path
	})
	return formatDirectorySearchMatches(root, mode, query, matches, total, args.ContextLines, args.MaxMatches, args.Index, warnings)
}

// directoryMatchHeap keeps the last match in path/line order at the root,
// retaining a bounded prefix while every match is counted.
type directoryMatchHeap []directorySearchMatch

func (h directoryMatchHeap) Len() int { return len(h) }
func (h directoryMatchHeap) Less(i, j int) bool {
	if h[i].Path == h[j].Path {
		return h[i].Line > h[j].Line
	}
	return h[i].Path > h[j].Path
}
func (h directoryMatchHeap) Swap(i, j int)   { h[i], h[j] = h[j], h[i] }
func (h *directoryMatchHeap) Push(value any) { *h = append(*h, value.(directorySearchMatch)) }
func (h *directoryMatchHeap) Pop() any {
	last := len(*h) - 1
	value := (*h)[last]
	(*h)[last] = directorySearchMatch{}
	*h = (*h)[:last]
	return value
}

func findDirectoryGrepMatches(ctx context.Context, root, query string, keep int, glob []string) (operationresult.Result[[]directorySearchMatch], int, []string, error) {
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
	matches := directoryMatchHeap{}
	total, err := scanDirectoryGrepBatch(ctx, root, pattern, files.Data, keep, &matches)
	if err != nil {
		return operationresult.Result[[]directorySearchMatch]{}, 0, nil, err
	}
	if total.Status != operationresult.Success {
		return operationresult.Result[[]directorySearchMatch]{Status: total.Status, Message: total.Message}, 0, nil, nil
	}
	return operationresult.Result[[]directorySearchMatch]{Status: operationresult.Success, Data: []directorySearchMatch(matches)}, total.Data, warnings, nil
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

func scanDirectoryGrepBatch(ctx context.Context, root string, query *regexp.Regexp, files []string, keep int, matches *directoryMatchHeap) (operationresult.Result[int], error) {
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
				for _, line := range fileops.SplitLines(text) {
					lineNumber++
					location := query.FindStringIndex(line)
					if location == nil {
						continue
					}
					count++
					if len(candidates) < keep {
						candidates = append(candidates, directorySearchMatch{Path: rel, Line: lineNumber, Column: location[0] + 1, EndLine: lineNumber, Kind: "grep"})
					}
				}
			}
			for _, match := range candidates {
				if matches.Len() < keep {
					heap.Push(matches, match)
				} else if match.Path < (*matches)[0].Path || (match.Path == (*matches)[0].Path && match.Line < (*matches)[0].Line) {
					(*matches)[0] = match
					heap.Fix(matches, 0)
				}
			}
			return operationresult.Result[int]{Status: operationresult.Success, Data: count}, nil
		}()
		if err != nil {
			return operationresult.Result[int]{}, err
		}
		if count.Status != operationresult.Success {
			return count, nil
		}
		total += count.Data
	}
	return operationresult.Result[int]{Status: operationresult.Success, Data: total}, nil
}

// readDirectoryASTSource reads unsupported extensions only far enough to identify
// a shell shebang, retaining the same reader when the remaining source is needed.
func readDirectoryASTSource(path string) (operationresult.Result[[]byte], error) {
	if _, _, err := detectASTLanguage(path, ""); err == nil {
		data, err := os.ReadFile(path)
		if err != nil {
			return operationresult.Result[[]byte]{}, err
		}
		return operationresult.Result[[]byte]{Status: operationresult.Success, Data: data}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return operationresult.Result[[]byte]{}, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	prefix, err := reader.Peek(2)
	if err != nil && err != io.EOF {
		return operationresult.Result[[]byte]{}, err
	}
	if string(prefix) != "#!" {
		return operationresult.Result[[]byte]{Status: operationresult.InvalidTarget, Message: "AST search only supports Go and Shell files"}, nil
	}
	firstLine, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return operationresult.Result[[]byte]{}, err
	}
	if _, _, err := detectASTLanguage(path, firstLine); err != nil {
		return operationresult.Result[[]byte]{Status: operationresult.InvalidTarget, Message: err.Error()}, nil
	}
	text, err := io.ReadAll(reader)
	if err != nil {
		return operationresult.Result[[]byte]{}, err
	}
	return operationresult.Result[[]byte]{Status: operationresult.Success, Data: append([]byte(firstLine), text...)}, nil
}

func findDirectoryASTMatches(root, query, mode string) ([]directorySearchMatch, []directoryASTFileState, error) {
	matches := make([]directorySearchMatch, 0)
	files := make([]directoryASTFileState, 0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, directoryASTFileState{Path: filepath.ToSlash(rel), Size: info.Size(), ModTime: info.ModTime().UnixNano()})
		if info.Size() > fileToolDetailedMaxBytes {
			return nil
		}
		loaded, err := readDirectoryASTSource(path)
		if err != nil {
			return err
		}
		if loaded.Status != operationresult.Success {
			return nil
		}
		text := loaded.Data
		language, variant, err := detectASTLanguage(path, string(text))
		if err != nil {
			return nil
		}
		if mode == readFileModeASTFunction {
			var functions []astFunctionMatch
			if language == "go" {
				functions, _, err = findGoASTFunctions(path, string(text), query)
			} else {
				functions, _, err = findShellASTFunctions(string(text), query, variant)
			}
			if err != nil {
				return nil
			}
			for _, function := range functions {
				matches = append(matches, directorySearchMatch{Path: filepath.ToSlash(rel), Line: function.StartLine, EndLine: function.EndLine, Kind: function.Kind, Label: function.Name})
			}
			return nil
		}
		var identifiers []astMatch
		if language == "go" {
			identifiers, _, err = findGoASTMatches(path, string(text), query)
		} else {
			identifiers, _, err = findShellASTMatches(string(text), query, variant)
		}
		if err != nil {
			return nil
		}
		for _, identifier := range identifiers {
			matches = append(matches, directorySearchMatch{Path: filepath.ToSlash(rel), Line: identifier.Line, Column: identifier.Column, EndLine: identifier.Line, Kind: identifier.Kind, Label: identifier.Container})
		}
		return nil
	})
	return matches, files, err
}

func directoryASTFilesMatch(root string, cached []directoryASTFileState) bool {
	current := make([]directoryASTFileState, 0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		current = append(current, directoryASTFileState{Path: filepath.ToSlash(rel), Size: info.Size(), ModTime: info.ModTime().UnixNano()})
		return nil
	})
	if err != nil || len(current) != len(cached) {
		return false
	}
	sort.Slice(current, func(i, j int) bool { return current[i].Path < current[j].Path })
	for i, file := range current {
		if file != cached[i] {
			return false
		}
	}
	return true
}

func formatDirectorySearchMatches(root, mode, query string, matches []directorySearchMatch, total, contextLines, maxMatches, index int, warnings []string) (operationresult.Result[*toolshare.Result], error) {
	maxMatches = fileops.NormalizeMaxMatches(maxMatches)
	if index > total || index < 0 {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("index %d is out of range; found %d matches", index, total)}, nil
	}
	truncated := index == 0 && total > maxMatches
	shown := matches
	if index > 0 {
		shown = matches[index-1 : index]
	} else if truncated {
		shown = matches[:maxMatches]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "path: %s\nmode: %s\nquery: %q\nmatches: %d\ntruncated: %t\n", root, mode, query, total, truncated)
	if len(matches) == 0 {
		b.WriteString("content:\n")
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Content: b.String(), Warnings: warnings}}, nil
	}
	if index > 0 {
		fmt.Fprintf(&b, "index: %d\ncontent:\n", index)
		return formatDirectorySearchSelection(&b, root, mode, shown[0], contextLines, warnings)
	}
	if len(matches) > 1 || mode != readFileModeASTFunction {
		if mode == readFileModeASTFunction && len(matches) > 1 {
			b.WriteString("selection_required: true\n")
		}
		b.WriteString("content:\n")
		for i, match := range shown {
			if mode == readFileModeASTFunction {
				fmt.Fprintf(&b, "%d. %s - %s:%d-%d\n", i+1, match.Label, match.Path, match.Line, match.EndLine)
			} else {
				fmt.Fprintf(&b, "%d. %s:%d:%d [%s] %s\n", i+1, match.Path, match.Line, match.Column, match.Kind, match.Label)
			}
		}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Content: b.String(), Warnings: warnings}}, nil
	}
	b.WriteString("content:\n")
	return formatDirectorySearchSelection(&b, root, mode, shown[0], contextLines, warnings)
}

func formatDirectorySearchSelection(b *strings.Builder, root, mode string, match directorySearchMatch, contextLines int, warnings []string) (operationresult.Result[*toolshare.Result], error) {
	path := filepath.Join(root, filepath.FromSlash(match.Path))
	maxBytes := int64(fileops.MaxFileSize)
	if mode == readFileModeGrep {
		maxBytes = fileToolMaxBytes
	}
	loaded, err := fileops.ReadFileWithLimit(path, "", maxBytes)
	if err != nil {
		return operationresult.Result[*toolshare.Result]{}, err
	}
	if loaded.Status != operationresult.Success {
		return operationresult.Result[*toolshare.Result]{Status: loaded.Status, Message: loaded.Message}, nil
	}
	file := loaded.Data
	lines := fileops.SplitLines(file.Text)
	if match.Line < 1 || match.Line > len(lines) || match.EndLine > len(lines) {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Conflict, Message: "file changed after directory search"}, nil
	}
	if int64(len(file.Bytes)) > fileToolDetailedMaxBytes {
		warnings = append(warnings, fmt.Sprintf("large file: %d bytes; ast and ast_function modes are disabled", len(file.Bytes)))
	}
	start, end := match.Line, match.EndLine
	if mode != readFileModeASTFunction {
		contextLines = fileops.NormalizeGrepContextLines(contextLines)
		start, end = max(1, match.Line-contextLines), min(len(lines), match.Line+contextLines)
	}
	fmt.Fprintf(b, "match: %s:%d-%d [%s] %s\n", match.Path, match.Line, match.EndLine, match.Kind, match.Label)
	width := len(fmt.Sprintf("%d", len(lines)))
	for line := start; line <= end; line++ {
		fmt.Fprintf(b, "  %*d | %s\n", width, line, lines[line-1])
	}
	return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Content: b.String(), Warnings: warnings}}, nil
}
