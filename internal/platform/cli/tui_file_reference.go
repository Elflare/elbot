package cli

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"elbot/internal/completion"
)

const (
	kindLocalFileReference        = "local_file"
	maxLocalFileCompletionItems   = 50
	maxLocalFileCompletionScan    = 5000
	defaultLocalFileMaxSize       = 256 * 1024
	defaultLocalFileMaxTotalSize  = 1024 * 1024
	localFileReferenceDescription = "local file"
)

var localFileIgnoredDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
}

type localFileResolver struct {
	root        string
	maxFileSize int64
	maxTotal    int64
}

type localFileReferenceToken struct {
	Start  int
	End    int
	Query  string
	Quoted bool
}

type localFileCandidate struct {
	path  string
	score int
}

func newLocalFileResolver(root string) *localFileResolver {
	if strings.TrimSpace(root) == "" {
		wd, err := os.Getwd()
		if err == nil {
			root = wd
		}
	}
	abs, err := filepath.Abs(root)
	if err == nil {
		root = abs
	}
	return &localFileResolver{root: root, maxFileSize: defaultLocalFileMaxSize, maxTotal: defaultLocalFileMaxTotalSize}
}

func (r *localFileResolver) Complete(ctx context.Context, req completion.Request) []completion.Item {
	_ = ctx
	if r == nil || strings.TrimSpace(r.root) == "" {
		return nil
	}
	token, ok := parseLocalFileCompletionToken(req.Text, req.CursorOrEnd())
	if !ok {
		return nil
	}
	candidates := r.matchingFiles(token.Query)
	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) > maxLocalFileCompletionItems {
		candidates = candidates[:maxLocalFileCompletionItems]
	}
	items := make([]completion.Item, 0, len(candidates))
	for _, candidate := range candidates {
		text := formatLocalFileReference(candidate.path, token.Quoted)
		items = append(items, completion.Item{
			Text:         text,
			Label:        candidate.path,
			Description:  localFileReferenceDescription,
			Kind:         kindLocalFileReference,
			ReplaceStart: token.Start,
			ReplaceEnd:   token.End,
		})
	}
	return items
}

func parseLocalFileCompletionToken(text string, cursor int) (localFileReferenceToken, bool) {
	if cursor < 0 || cursor > len(text) {
		cursor = len(text)
	}
	for start := cursor - 1; start >= 0; start-- {
		if text[start] != '#' || start+1 >= len(text) || text[start+1] != '"' {
			continue
		}
		if start > 0 && !isReferenceBoundary(text[start-1]) {
			continue
		}
		if strings.Contains(text[start+2:cursor], "\"") {
			continue
		}
		return localFileReferenceToken{Start: start, End: cursor, Query: text[start+2 : cursor], Quoted: true}, true
	}

	start := cursor
	for start > 0 && !isReferenceBoundary(text[start-1]) {
		start--
	}
	if start >= cursor || text[start] != '#' {
		return localFileReferenceToken{}, false
	}
	if start+1 < len(text) && text[start+1] == '"' {
		return localFileReferenceToken{}, false
	}
	return localFileReferenceToken{Start: start, End: cursor, Query: text[start+1 : cursor]}, true
}

func isReferenceBoundary(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

func (r *localFileResolver) matchingFiles(query string) []localFileCandidate {
	candidates := []localFileCandidate{}
	scanned := 0
	_ = filepath.WalkDir(r.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == r.root {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if localFileIgnoredDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if scanned >= maxLocalFileCompletionScan {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		scanned++
		rel, err := filepath.Rel(r.root, path)
		if err != nil {
			return nil
		}
		displayPath := filepath.ToSlash(rel)
		score, ok := localFileFuzzyScore(displayPath, query)
		if !ok {
			return nil
		}
		candidates = append(candidates, localFileCandidate{path: displayPath, score: score})
		return nil
	})
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if len(candidates[i].path) != len(candidates[j].path) {
			return len(candidates[i].path) < len(candidates[j].path)
		}
		return candidates[i].path < candidates[j].path
	})
	return candidates
}

func localFileFuzzyScore(path, query string) (int, bool) {
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
	if segmentPrefixMatch(pathLower, query) {
		return 8500 - len(pathLower), true
	}
	if index := strings.Index(pathLower, query); index >= 0 {
		return 7600 - index - len(pathLower), true
	}
	if index := strings.Index(baseLower, query); index >= 0 {
		return 7400 - index - len(pathLower), true
	}
	score, ok := subsequenceScore(pathLower, query)
	if !ok {
		return 0, false
	}
	return score - len(pathLower), true
}

func segmentPrefixMatch(path, query string) bool {
	for _, segment := range strings.Split(path, "/") {
		if strings.HasPrefix(segment, query) {
			return true
		}
	}
	return false
}

func subsequenceScore(value, query string) (int, bool) {
	qi := 0
	lastMatch := -1
	adjacent := 0
	gaps := 0
	for i := 0; i < len(value) && qi < len(query); i++ {
		if value[i] != query[qi] {
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
		qi++
	}
	if qi != len(query) {
		return 0, false
	}
	return 5000 + adjacent*20 - gaps*2, true
}

func formatLocalFileReference(path string, forceQuoted bool) string {
	if forceQuoted || strings.ContainsAny(path, " \t\r\n") {
		return "#\"" + path + "\""
	}
	return "#" + path
}

func (r *localFileResolver) expandReferences(text string) (string, error) {
	if r == nil {
		return text, nil
	}
	var out strings.Builder
	total := int64(0)
	changed := false
	for i := 0; i < len(text); {
		if text[i] != '#' {
			rn, size := utf8.DecodeRuneInString(text[i:])
			if rn == utf8.RuneError && size == 1 {
				out.WriteByte(text[i])
				i++
				continue
			}
			out.WriteString(text[i : i+size])
			i += size
			continue
		}
		refPath, end, ok, err := parseLocalFileReferenceAt(text, i)
		if err != nil {
			return "", err
		}
		if !ok {
			out.WriteByte(text[i])
			i++
			continue
		}
		expansion, err := r.expandReference(refPath, &total)
		if err != nil {
			return "", err
		}
		out.WriteString(expansion)
		i = end
		changed = true
	}
	if !changed {
		return text, nil
	}
	return out.String(), nil
}

func parseLocalFileReferenceAt(text string, start int) (string, int, bool, error) {
	if start > 0 && !isReferenceBoundary(text[start-1]) {
		return "", 0, false, nil
	}
	if start+1 < len(text) && text[start+1] == '"' {
		end := start + 2
		for end < len(text) && text[end] != '"' {
			_, size := utf8.DecodeRuneInString(text[end:])
			end += size
		}
		if end >= len(text) {
			return "", 0, false, fmt.Errorf("unterminated local file reference")
		}
		refPath := text[start+2 : end]
		if strings.TrimSpace(refPath) == "" {
			return "", 0, false, nil
		}
		return refPath, end + 1, true, nil
	}
	end := start + 1
	for end < len(text) {
		rn, size := utf8.DecodeRuneInString(text[end:])
		if unicode.IsSpace(rn) {
			break
		}
		end += size
	}
	if end == start+1 {
		return "", 0, false, nil
	}
	return text[start+1 : end], end, true, nil
}

func (r *localFileResolver) expandReference(refPath string, total *int64) (string, error) {
	displayPath := filepath.ToSlash(refPath)
	clean := filepath.Clean(filepath.FromSlash(refPath))
	if clean == "." || clean == string(filepath.Separator) || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("local file reference %q is outside the current directory", refPath)
	}
	abs := filepath.Join(r.root, clean)
	rel, err := filepath.Rel(r.root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("local file reference %q is outside the current directory", refPath)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("read local file reference %q: %w", refPath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("local file reference %q is a directory", refPath)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("local file reference %q is not a regular file", refPath)
	}
	maxFileSize := r.maxFileSize
	if maxFileSize <= 0 {
		maxFileSize = defaultLocalFileMaxSize
	}
	if info.Size() > maxFileSize {
		return "", fmt.Errorf("local file reference %q is too large (%d bytes > %d bytes)", refPath, info.Size(), maxFileSize)
	}
	maxTotal := r.maxTotal
	if maxTotal <= 0 {
		maxTotal = defaultLocalFileMaxTotalSize
	}
	if *total+info.Size() > maxTotal {
		return "", fmt.Errorf("local file references are too large (%d bytes > %d bytes)", *total+info.Size(), maxTotal)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read local file reference %q: %w", refPath, err)
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return "", fmt.Errorf("local file reference %q appears to be binary", refPath)
	}
	*total += int64(len(data))
	return formatLocalFileExpansion(displayPath, string(data)), nil
}

func formatLocalFileExpansion(path, content string) string {
	fence := "```"
	for strings.Contains(content, fence) {
		fence += "`"
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return fmt.Sprintf("[file: %s]\n%stext\n%s%s", path, fence, content, fence)
}
