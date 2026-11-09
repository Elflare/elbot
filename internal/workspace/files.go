package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxFileSearchItems = 50
	maxFileSearchScan  = 5000
	maxReferencedBytes = 256 * 1024
	maxReferencesBytes = 1024 * 1024
)

var ignoredFileSearchDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
}

type fileCandidate struct {
	file  WorkspaceFile
	score int
}

type FileReferenceToken struct {
	Start  int
	End    int
	Query  string
	Quoted bool
}

func SearchWorkspaceFiles(ctx context.Context, rootPath, query string) ([]WorkspaceFile, error) {
	rootPath, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	var candidates []fileCandidate
	scanned := 0
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		if entry.IsDir() {
			if ignoredFileSearchDirs[entry.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if scanned == maxFileSearchScan {
			return fs.SkipAll
		}
		scanned++
		rel := filepath.ToSlash(path)
		if score, ok := fileSearchScore(rel, query); ok {
			candidates = append(candidates, fileCandidate{
				file:  WorkspaceFile{Name: entry.Name(), Path: rel, Kind: "file", Size: info.Size()},
				score: score,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if len(candidates[i].file.Path) != len(candidates[j].file.Path) {
			return len(candidates[i].file.Path) < len(candidates[j].file.Path)
		}
		return candidates[i].file.Path < candidates[j].file.Path
	})
	if len(candidates) > maxFileSearchItems {
		candidates = candidates[:maxFileSearchItems]
	}
	files := make([]WorkspaceFile, len(candidates))
	for i, candidate := range candidates {
		files[i] = candidate.file
	}
	return files, nil
}

func ParseFileReferenceToken(text string, cursor int) (FileReferenceToken, bool) {
	if cursor < 0 || cursor > len(text) {
		cursor = len(text)
	}
	for start := cursor - 1; start >= 0; start-- {
		if text[start] != '#' || start+1 >= len(text) || text[start+1] != '"' || !referenceBoundary(text, start) {
			continue
		}
		value := text[start+2 : cursor]
		if hasUnescapedQuote(value) {
			continue
		}
		return FileReferenceToken{Start: start, End: cursor, Query: unescapeFileReference(value), Quoted: true}, true
	}
	start := cursor
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:start])
		if unicode.IsSpace(r) {
			break
		}
		start -= size
	}
	if start >= cursor || text[start] != '#' || start+1 < len(text) && text[start+1] == '"' {
		return FileReferenceToken{}, false
	}
	return FileReferenceToken{Start: start, End: cursor, Query: text[start+1 : cursor]}, true
}

func FormatFileReference(path string, forceQuoted bool) string {
	if forceQuoted || strings.IndexFunc(path, func(r rune) bool { return unicode.IsSpace(r) || r == '"' }) >= 0 {
		return `#"` + escapeFileReference(path) + `"`
	}
	return "#" + path
}

func escapeFileReference(path string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(path)
}

func unescapeFileReference(path string) string {
	var out strings.Builder
	for i := 0; i < len(path); i++ {
		if path[i] == '\\' && i+1 < len(path) && (path[i+1] == '\\' || path[i+1] == '"') {
			i++
		}
		out.WriteByte(path[i])
	}
	return out.String()
}

func hasUnescapedQuote(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] == '\\' && i+1 < len(value) && (value[i+1] == '\\' || value[i+1] == '"') {
			i++
			continue
		}
		if value[i] == '"' {
			return true
		}
	}
	return false
}

func ExpandFileReferences(ctx context.Context, rootPath, text string) (string, error) {
	if !strings.Contains(text, "#") {
		return text, nil
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return "", err
	}
	defer root.Close()

	var out strings.Builder
	var total int64
	for i := 0; i < len(text); {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if text[i] != '#' {
			r, size := utf8.DecodeRuneInString(text[i:])
			if r == utf8.RuneError && size == 1 {
				out.WriteByte(text[i])
				i++
				continue
			}
			out.WriteString(text[i : i+size])
			i += size
			continue
		}
		path, end, ok, err := parseFileReference(text, i)
		if err != nil {
			return "", err
		}
		if !ok {
			out.WriteByte(text[i])
			i++
			continue
		}
		replacement, expanded, err := expandFileReference(root, path, &total)
		if errors.Is(err, fs.ErrNotExist) {
			out.WriteString(text[i:end])
			i = end
			continue
		}
		if err != nil {
			return "", err
		}
		if !expanded {
			replacement = path
		}
		out.WriteString(replacement)
		i = end
	}
	return out.String(), nil
}

func FormatFileExpansion(path, content string) string {
	fence := "```"
	for strings.Contains(content, fence) {
		fence += "`"
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return fmt.Sprintf("[file: %s]\n%stext\n%s%s", path, fence, content, fence)
}

func fileSearchScore(path, query string) (int, bool) {
	query = strings.ToLower(strings.TrimSpace(filepath.ToSlash(query)))
	pathLower := strings.ToLower(path)
	baseLower := strings.ToLower(filepath.Base(pathLower))
	if query == "" {
		return 1000 - len(pathLower), true
	}
	if pathLower == query || baseLower == query {
		return 10000 - len(pathLower), true
	}
	if strings.HasPrefix(pathLower, query) {
		return 9000 - len(pathLower), true
	}
	if strings.HasPrefix(baseLower, query) {
		return 8800 - len(pathLower), true
	}
	for _, segment := range strings.Split(pathLower, "/") {
		if strings.HasPrefix(segment, query) {
			return 8500 - len(pathLower), true
		}
	}
	if index := strings.Index(pathLower, query); index >= 0 {
		return 7600 - index - len(pathLower), true
	}
	if index := strings.Index(baseLower, query); index >= 0 {
		return 7400 - index - len(pathLower), true
	}
	score, ok := subsequenceScore(pathLower, query)
	return score - len(pathLower), ok
}

func subsequenceScore(value, query string) (int, bool) {
	queryIndex, lastMatch, adjacent, gaps := 0, -1, 0, 0
	for i := 0; i < len(value) && queryIndex < len(query); i++ {
		if value[i] != query[queryIndex] {
			continue
		}
		if lastMatch >= 0 {
			if i == lastMatch+1 {
				adjacent++
			} else {
				gaps += i - lastMatch - 1
			}
		}
		lastMatch = i
		queryIndex++
	}
	if queryIndex != len(query) {
		return 0, false
	}
	return 5000 + adjacent*20 - gaps*2, true
}

func parseFileReference(text string, start int) (string, int, bool, error) {
	if !referenceBoundary(text, start) {
		return "", 0, false, nil
	}
	if start+1 < len(text) && text[start+1] == '"' {
		for end := start + 2; end < len(text); end++ {
			if text[end] == '\\' && end+1 < len(text) && (text[end+1] == '\\' || text[end+1] == '"') {
				end++
				continue
			}
			if text[end] == '"' {
				path := unescapeFileReference(text[start+2 : end])
				if strings.TrimSpace(path) == "" {
					return "", 0, false, nil
				}
				return path, end + 1, true, nil
			}
		}
		return "", 0, false, fmt.Errorf("unterminated workspace file reference")
	}
	end := start + 1
	for end < len(text) {
		r, size := utf8.DecodeRuneInString(text[end:])
		if unicode.IsSpace(r) {
			break
		}
		end += size
	}
	if end == start+1 {
		return "", 0, false, nil
	}
	return text[start+1 : end], end, true, nil
}

func expandFileReference(root *os.Root, path string, total *int64) (string, bool, error) {
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("workspace file reference %q is outside the workspace", path)
	}
	file, err := root.Open(clean)
	if err != nil {
		return "", false, fmt.Errorf("read workspace file reference %q: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", false, fmt.Errorf("read workspace file reference %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("workspace file reference %q is not a regular file", path)
	}
	if info.Size() > maxReferencedBytes || *total+info.Size() > maxReferencesBytes {
		return path, false, nil
	}
	data, err := io.ReadAll(io.LimitReader(file, maxReferencedBytes+1))
	if err != nil {
		return "", false, fmt.Errorf("read workspace file reference %q: %w", path, err)
	}
	if int64(len(data)) > maxReferencedBytes || *total+int64(len(data)) > maxReferencesBytes || bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return path, false, nil
	}
	*total += int64(len(data))
	return FormatFileExpansion(filepath.ToSlash(path), string(data)), true, nil
}

func referenceBoundary(text string, start int) bool {
	if start == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:start])
	return unicode.IsSpace(r)
}
