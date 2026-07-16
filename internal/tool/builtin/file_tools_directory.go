package builtin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"elbot/internal/tool"
	"elbot/internal/utils/fileops"
)

type directorySearchMatch struct {
	Path     string
	Line     int
	Column   int
	EndLine  int
	Kind     string
	Label    string
	Text     string
	Language string
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

func (c *directoryASTCache) load(root, mode, query string) ([]directorySearchMatch, bool) {
	if c == nil {
		return nil, false
	}
	key := directoryASTCacheKey{Root: root, Mode: mode, Query: query}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.ExpiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	if !directoryASTFilesMatch(root, entry.Files) {
		delete(c.entries, key)
		return nil, false
	}
	entry.UsedAt = time.Now()
	c.entries[key] = entry
	return append([]directorySearchMatch(nil), entry.Matches...), true
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

func readFileDirectorySearch(ctx context.Context, root, mode string, args readFileArgs, warnings []string, astCache *directoryASTCache) (*tool.Result, error) {
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required when mode is %s", mode)
	}
	var matches []directorySearchMatch
	var err error
	switch mode {
	case readFileModeGrep:
		matches, err = findDirectoryGrepMatches(ctx, root, query)
	case readFileModeAST, readFileModeASTFunction:
		if cached, ok := astCache.load(root, mode, query); ok {
			matches = cached
		} else {
			var files []directoryASTFileState
			matches, files, err = findDirectoryASTMatches(root, query, mode)
			if err == nil {
				astCache.store(root, mode, query, matches, files)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Path == matches[j].Path {
			return matches[i].Line < matches[j].Line
		}
		return matches[i].Path < matches[j].Path
	})
	return formatDirectorySearchMatches(root, mode, query, matches, args.ContextLines, args.MaxMatches, args.Index, warnings)
}

func findDirectoryGrepMatches(ctx context.Context, root, query string) ([]directorySearchMatch, error) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		return nil, fmt.Errorf("directory grep requires ripgrep (rg), but rg was not found in PATH. Ask the user to install ripgrep and make `rg` available in PATH")
	}
	cmd := exec.CommandContext(ctx, rg, "--json", "--fixed-strings", "--no-messages", "--glob", "!**/.git/**", "--glob", "!**/node_modules/**", "--glob", "!**/vendor/**", "--", query, root)
	output, err := cmd.Output()
	if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() != 1 {
		return nil, fmt.Errorf("run ripgrep: %w", err)
	} else if err != nil && !ok {
		return nil, fmt.Errorf("run ripgrep: %w", err)
	}
	matches := make([]directorySearchMatch, 0)
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
			Data struct {
				Path struct {
					Text string `json:"text"`
				} `json:"path"`
				LineNumber int `json:"line_number"`
				Lines      struct {
					Text string `json:"text"`
				} `json:"lines"`
				Submatches []struct {
					Start int `json:"start"`
				} `json:"submatches"`
			} `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil || event.Type != "match" {
			continue
		}
		column := 1
		if len(event.Data.Submatches) > 0 {
			column += event.Data.Submatches[0].Start
		}
		path, err := filepath.Rel(root, event.Data.Path.Text)
		if err != nil {
			continue
		}
		matches = append(matches, directorySearchMatch{Path: filepath.ToSlash(path), Line: event.Data.LineNumber, Column: column, EndLine: event.Data.LineNumber, Kind: "grep", Text: strings.TrimSuffix(event.Data.Lines.Text, "\n")})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read ripgrep output: %w", err)
	}
	return matches, nil
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
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		files = append(files, directoryASTFileState{Path: filepath.ToSlash(rel), Size: info.Size(), ModTime: info.ModTime().UnixNano()})
		text, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
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
				matches = append(matches, directorySearchMatch{Path: filepath.ToSlash(rel), Line: function.StartLine, EndLine: function.EndLine, Kind: function.Kind, Label: function.Name, Language: language})
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
			matches = append(matches, directorySearchMatch{Path: filepath.ToSlash(rel), Line: identifier.Line, Column: identifier.Column, EndLine: identifier.Line, Kind: identifier.Kind, Label: identifier.Container, Language: language})
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

func formatDirectorySearchMatches(root, mode, query string, matches []directorySearchMatch, contextLines, maxMatches, index int, warnings []string) (*tool.Result, error) {
	maxMatches = fileops.NormalizeMaxMatches(maxMatches)
	if index > len(matches) || index < 0 {
		return nil, fmt.Errorf("index %d is out of range; found %d matches", index, len(matches))
	}
	truncated := index == 0 && len(matches) > maxMatches
	shown := matches
	if index > 0 {
		shown = matches[index-1 : index]
	} else if truncated {
		shown = matches[:maxMatches]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "path: %s\nmode: %s\nquery: %q\nmatches: %d\ntruncated: %t\n", root, mode, query, len(matches), truncated)
	if len(matches) == 0 {
		b.WriteString("content:\n")
		return &tool.Result{Content: b.String(), Warnings: warnings}, nil
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
		return &tool.Result{Content: b.String(), Warnings: warnings}, nil
	}
	b.WriteString("content:\n")
	return formatDirectorySearchSelection(&b, root, mode, shown[0], contextLines, warnings)
}

func formatDirectorySearchSelection(b *strings.Builder, root, mode string, match directorySearchMatch, contextLines int, warnings []string) (*tool.Result, error) {
	path := filepath.Join(root, filepath.FromSlash(match.Path))
	file, err := fileops.ReadFile(path, "")
	if err != nil {
		return nil, err
	}
	lines := fileops.SplitLines(file.Text)
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
	return &tool.Result{Content: b.String(), Warnings: warnings}, nil
}
