package fileops

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
)

const (
	DefaultReadLineLimit = 200
	MaxFileSize          = 2 * 1024 * 1024
	contentRevisionBytes = 8
	maxDiffCells         = 2_000_000
	matchPreviewLimit    = 5
	matchPreviewWidth    = 40
)

type LineNumber struct {
	Value int
	End   bool
}

func (n *LineNumber) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "" || text == "null" {
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		str = strings.ToLower(strings.TrimSpace(str))
		if str == "" {
			return nil
		}
		if str == "end" {
			n.End = true
			n.Value = 0
			return nil
		}
		value, err := strconv.Atoi(str)
		if err != nil {
			return fmt.Errorf("line number string must be integer or \"end\"")
		}
		n.Value = value
		n.End = false
		return nil
	}
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("line number must be integer or \"end\"")
	}
	n.Value = value
	n.End = false
	return nil
}

type File struct {
	Path        string
	Bytes       []byte
	Text        string
	Encoding    string
	BOM         []byte
	LineEnding  string
	EndsNewline bool
}

type Edit struct {
	Operation  string  `json:"operation"`
	Line       int     `json:"line"`
	EndLine    int     `json:"end_line"`
	NewText    *string `json:"new_text"`
	OldText    string  `json:"old_text"`
	Anchor     string  `json:"anchor"`
	Index      *int    `json:"index"`
	AllMatches bool    `json:"all_matches"`
}

type EditResult struct {
	DryRun         bool
	Path           string
	Created        bool
	Encoding       string
	RevisionBefore string
	RevisionAfter  string
	Diff           string
	NewBytes       []byte
}

func EditOperationProperties() map[string]any {
	return map[string]any{
		"operation":   map[string]any{"type": "string", "enum": []string{"replace_text", "replace", "insert", "delete", "insert_before", "insert_after", "replace_line", "delete_line", "overwrite"}, "description": "编辑操作。所有目标都基于编辑前原文解析；overwrite 必须是批次中的唯一操作。"},
		"line":        map[string]any{"type": "integer", "description": "replace/delete 的起始行或 insert 的行间位置，1-based。insert: 1=文件开头，N+1=文件末尾；空文件仅支持 1。"},
		"end_line":    map[string]any{"type": "integer", "description": "replace/delete 的可选结束行，1-based 且包含该行；省略时只操作 line。"},
		"new_text":    map[string]any{"type": "string", "description": "写入文本。replace_text/replace/overwrite 使用原样文本；replace 不自动补换行，若需换行，需手动添加。insert/insert_before/insert_after/replace_line 把内容作为完整行块，缩进需手动，换行自动追加。replace_text 和 replace 可传空字符串进行删除。"},
		"old_text":    map[string]any{"type": "string", "description": "replace_text 的精确单行或多行匹配文本。"},
		"anchor":      map[string]any{"type": "string", "description": "insert_before/insert_after/replace_line/delete_line 的单行前缀；匹配时忽略目标行的前导空格和 Tab。匹配多行但没传入index时，会返回index"},
		"index":       map[string]any{"type": "integer", "description": "匹配到多处时选择第几处，1-based。与 all_matches 互斥。"},
		"all_matches": map[string]any{"type": "boolean", "description": "对所有匹配执行操作。与 index 互斥；仅用于 old_text/anchor 操作。"},
	}
}

func ReadFile(path, requestedEncoding string) (File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return File{}, fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() {
		return File{}, fmt.Errorf("path is a directory")
	}
	if info.Size() > MaxFileSize {
		return File{}, fmt.Errorf("file too large: %d bytes exceeds %d", info.Size(), MaxFileSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("read file: %w", err)
	}
	if LooksBinary(data) {
		return File{}, fmt.Errorf("file appears to be binary")
	}
	text, encName, bom, err := DecodeBytes(data, requestedEncoding)
	if err != nil {
		return File{}, err
	}
	return File{Path: path, Bytes: data, Text: text, Encoding: encName, BOM: bom, LineEnding: DetectLineEnding(text), EndsNewline: strings.HasSuffix(text, "\n")}, nil
}

func ReadOrCreateFile(path, requestedEncoding string, create bool, hasExpectedRevision bool) (File, bool, error) {
	file, err := ReadFile(path, requestedEncoding)
	if err == nil {
		return file, false, nil
	}
	if !create || hasExpectedRevision || !errors.Is(err, os.ErrNotExist) {
		return File{}, false, err
	}
	encodingName := strings.ToLower(strings.TrimSpace(requestedEncoding))
	if encodingName == "" || encodingName == "auto" || encodingName == "utf8" {
		encodingName = "utf-8"
	}
	if encodingName == "utf-8-bom" {
		return File{Path: path, Encoding: "utf-8-bom", BOM: []byte{0xEF, 0xBB, 0xBF}, LineEnding: "\n"}, true, nil
	}
	if encodingName != "utf-8" {
		if _, err := LookupEncoding(encodingName); err != nil {
			return File{}, false, err
		}
	}
	return File{Path: path, Encoding: encodingName, LineEnding: "\n"}, true, nil
}

func EditFile(path, requestedEncoding, expectedRevision string, create, dryRun bool, contextLines int, edits []Edit) (EditResult, error) {
	expectedRevision = strings.TrimSpace(expectedRevision)
	if expectedRevision != "" {
		if len(expectedRevision) != contentRevisionBytes*2 {
			return EditResult{}, fmt.Errorf("expected_revision must be %d hexadecimal characters", contentRevisionBytes*2)
		}
		if _, err := hex.DecodeString(expectedRevision); err != nil {
			return EditResult{}, fmt.Errorf("expected_revision must be %d hexadecimal characters", contentRevisionBytes*2)
		}
	}
	file, created, err := ReadOrCreateFile(path, requestedEncoding, create, expectedRevision != "")
	if err != nil {
		return EditResult{}, err
	}
	oldRevision := ContentRevision(file.Bytes)
	if expectedRevision != "" && !strings.EqualFold(expectedRevision, oldRevision) {
		return EditResult{}, fmt.Errorf("file revision mismatch: current %s", oldRevision)
	}
	if !created && expectedRevision == "" && editsRequireRevision(edits) {
		return EditResult{}, fmt.Errorf("expected_revision is required for replace, insert, delete, and overwrite on existing files")
	}
	oldText := NormalizeEditText(file.Text)
	newText, err := ApplyEdits(oldText, edits)
	if err != nil {
		return EditResult{}, err
	}
	if newText == oldText {
		return EditResult{}, fmt.Errorf("edit produced no changes")
	}
	outputText := RestoreLineEndings(newText, file.LineEnding)
	newBytes, err := EncodeText(outputText, file.Encoding, file.BOM)
	if err != nil {
		return EditResult{}, err
	}
	contextLines = NormalizeContextLines(contextLines)
	result := EditResult{
		DryRun:         dryRun,
		Path:           file.Path,
		Created:        created,
		Encoding:       file.Encoding,
		RevisionBefore: oldRevision,
		RevisionAfter:  ContentRevision(newBytes),
		Diff:           UnifiedDiff(file.Path, SplitLines(oldText), SplitLines(newText), contextLines),
		NewBytes:       newBytes,
	}
	if dryRun {
		return result, nil
	}
	if created {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return EditResult{}, fmt.Errorf("create parent directory: %w", err)
		}
	}
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	} else if !created {
		return EditResult{}, fmt.Errorf("stat file: %w", err)
	}
	if err := AtomicWriteFile(path, newBytes, mode); err != nil {
		return EditResult{}, fmt.Errorf("write file: %w", err)
	}
	return result, nil
}

func WriteTextFile(path string, base File, text string) ([]byte, error) {
	outputText := RestoreLineEndings(text, base.LineEnding)
	newBytes, err := EncodeText(outputText, base.Encoding, base.BOM)
	if err != nil {
		return nil, err
	}
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	} else {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	if err := AtomicWriteFile(path, newBytes, mode); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}
	return newBytes, nil
}

func DecodeBytes(data []byte, requested string) (string, string, []byte, error) {
	name := strings.ToLower(strings.TrimSpace(requested))
	if name == "" || name == "auto" {
		switch {
		case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
			return string(data[3:]), "utf-8-bom", []byte{0xEF, 0xBB, 0xBF}, nil
		case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
			return decodeWithEncoding(data[2:], "utf-16le", []byte{0xFF, 0xFE})
		case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
			return decodeWithEncoding(data[2:], "utf-16be", []byte{0xFE, 0xFF})
		default:
			if !utf8.Valid(data) {
				return "", "", nil, fmt.Errorf("file is not valid UTF-8; pass encoding explicitly")
			}
			return string(data), "utf-8", nil, nil
		}
	}
	if name == "utf8" {
		name = "utf-8"
	}
	if name == "utf-8-bom" {
		if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
			data = data[3:]
		}
		if !utf8.Valid(data) {
			return "", "", nil, fmt.Errorf("file is not valid UTF-8")
		}
		return string(data), "utf-8-bom", []byte{0xEF, 0xBB, 0xBF}, nil
	}
	if name == "utf-8" {
		if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
			data = data[3:]
		}
		if !utf8.Valid(data) {
			return "", "", nil, fmt.Errorf("file is not valid UTF-8")
		}
		return string(data), "utf-8", nil, nil
	}
	return decodeWithEncoding(data, name, nil)
}

func decodeWithEncoding(data []byte, name string, bom []byte) (string, string, []byte, error) {
	enc, err := LookupEncoding(name)
	if err != nil {
		return "", "", nil, err
	}
	decoded, _, err := transform.Bytes(enc.NewDecoder(), data)
	if err != nil {
		return "", "", nil, fmt.Errorf("decode %s: %w", name, err)
	}
	return string(decoded), name, bom, nil
}

func EncodeText(text, name string, bom []byte) ([]byte, error) {
	if name == "utf-8" {
		return []byte(text), nil
	}
	if name == "utf-8-bom" {
		return append(append([]byte{}, bom...), []byte(text)...), nil
	}
	enc, err := LookupEncoding(name)
	if err != nil {
		return nil, err
	}
	encoded, _, err := transform.Bytes(enc.NewEncoder(), []byte(text))
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", name, err)
	}
	if len(bom) > 0 {
		encoded = append(append([]byte{}, bom...), encoded...)
	}
	return encoded, nil
}

func LookupEncoding(name string) (encoding.Encoding, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "gbk", "gb2312":
		name = "gb18030"
	case "shift_jis", "sjis":
		name = "shift-jis"
	}
	enc, err := htmlindex.Get(name)
	if err != nil || enc == nil {
		return nil, fmt.Errorf("unsupported encoding %q", name)
	}
	return enc, nil
}

func NormalizeReadRange(total, start int, endLine LineNumber) (int, int, bool, error) {
	if total == 0 {
		return 1, 0, false, nil
	}
	if start <= 0 {
		start = 1
	}
	if start > total {
		return 0, 0, false, fmt.Errorf("start_line %d exceeds total lines %d", start, total)
	}
	truncated := false
	end := endLine.Value
	if endLine.End {
		end = total
	} else if end <= 0 {
		end = start + DefaultReadLineLimit - 1
		truncated = end < total
	}
	if end > total {
		end = total
	}
	if end < start {
		return 0, 0, false, fmt.Errorf("end_line must be >= start_line")
	}
	if !endLine.End && end-start+1 > DefaultReadLineLimit {
		end = start + DefaultReadLineLimit - 1
		truncated = true
	}
	return start, end, truncated, nil
}

type matchLocation struct {
	byteStart int
	byteEnd   int
	startLine int
	endLine   int
	preview   string
	lineIndex int
}

func findContentMatches(text, needle string) []matchLocation {
	needleNorm := NormalizeEditText(needle)
	if needleNorm == "" {
		return nil
	}
	var locations []matchLocation
	offset := 0
	for {
		idx := strings.Index(text[offset:], needleNorm)
		if idx < 0 {
			break
		}
		start := offset + idx
		end := start + len(needleNorm)
		locations = append(locations, matchLocation{
			byteStart: start,
			byteEnd:   end,
			startLine: byteOffsetToLine(text, start),
			endLine:   byteOffsetToLine(text, end-1),
			preview:   matchPreview(text, start, end),
		})
		offset = end
	}
	return locations
}

func findLineMatches(lines []editSourceLine, needle string) []matchLocation {
	var locations []matchLocation
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line.text, " \t"), needle) {
			locations = append(locations, matchLocation{
				byteStart: line.start,
				byteEnd:   line.end,
				startLine: i + 1,
				endLine:   i + 1,
				preview:   truncateLine(strings.TrimLeft(line.text, " \t")),
				lineIndex: i,
			})
		}
	}
	return locations
}

func selectMatches(locations []matchLocation, index *int, all bool, label string) ([]matchLocation, error) {
	n := len(locations)
	if n == 0 {
		return nil, fmt.Errorf("%s not found", label)
	}
	if index != nil && all {
		return nil, fmt.Errorf("index and all_matches are mutually exclusive")
	}
	if all {
		return locations, nil
	}
	if index != nil {
		i := *index
		if i < 1 || i > n {
			return nil, fmt.Errorf("index %d out of range: %d %s found, use 1-%d", i, n, label, n)
		}
		return []matchLocation{locations[i-1]}, nil
	}
	if n > 1 {
		return nil, fmt.Errorf("%s matched %d locations; provide a longer %s, index, or all_matches:\n%s", label, n, label, formatMatchLocations(locations))
	}
	return locations, nil
}

func formatMatchLocations(locations []matchLocation) string {
	var b strings.Builder
	limit := len(locations)
	if limit > matchPreviewLimit {
		limit = matchPreviewLimit
	}
	for i := 0; i < limit; i++ {
		loc := locations[i]
		fmt.Fprintf(&b, "  #%d %s %q\n", i+1, formatLineRange(loc), loc.preview)
	}
	if len(locations) > limit {
		fmt.Fprintf(&b, "  ...and %d more\n", len(locations)-limit)
	}
	return b.String()
}

func formatLineRange(loc matchLocation) string {
	if loc.startLine == loc.endLine {
		return fmt.Sprintf("L%d", loc.startLine)
	}
	return fmt.Sprintf("L%d-%d", loc.startLine, loc.endLine)
}

func byteOffsetToLine(text string, offset int) int {
	if offset < 0 {
		offset = 0
	}
	if offset > len(text) {
		offset = len(text)
	}
	return strings.Count(text[:offset], "\n") + 1
}

func matchPreview(text string, start, end int) string {
	lineStart := start
	for lineStart > 0 && text[lineStart-1] != '\n' {
		lineStart--
	}
	return truncateLine(text[lineStart:end])
}

func truncateLine(s string) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > matchPreviewWidth {
		return s[:matchPreviewWidth] + "..."
	}
	return s
}

type editSourceLine struct {
	start   int
	end     int
	fullEnd int
	text    string
}

type resolvedEdit struct {
	start int
	end   int
	text  string
	order int
}

func editsRequireRevision(edits []Edit) bool {
	for _, edit := range edits {
		switch strings.ToLower(strings.TrimSpace(edit.Operation)) {
		case "replace", "insert", "delete", "overwrite":
			return true
		}
	}
	return false
}

func ApplyEdits(text string, edits []Edit) (string, error) {
	if len(edits) == 0 {
		return "", fmt.Errorf("edits is required")
	}
	text = NormalizeEditText(text)
	if strings.EqualFold(strings.TrimSpace(edits[0].Operation), "overwrite") {
		if len(edits) != 1 {
			return "", fmt.Errorf("edit 1: overwrite must be the only edit")
		}
		if err := validateEditFields(edits[0], "overwrite"); err != nil {
			return "", fmt.Errorf("edit 1: %w", err)
		}
		return NormalizeEditText(*edits[0].NewText), nil
	}

	lines := scanEditLines(text)
	resolved := make([]resolvedEdit, 0, len(edits))
	for i, edit := range edits {
		operation := strings.ToLower(strings.TrimSpace(edit.Operation))
		if operation == "overwrite" {
			return "", fmt.Errorf("edit %d: overwrite must be the only edit", i+1)
		}
		items, err := resolveEdit(text, lines, edit, operation)
		if err != nil {
			return "", fmt.Errorf("edit %d: %w", i+1, err)
		}
		for _, item := range items {
			item.order = len(resolved)
			resolved = append(resolved, item)
		}
	}
	if err := validateResolvedEdits(resolved); err != nil {
		return "", err
	}
	sort.SliceStable(resolved, func(i, j int) bool {
		if resolved[i].start != resolved[j].start {
			return resolved[i].start > resolved[j].start
		}
		iInsert := resolved[i].start == resolved[i].end
		jInsert := resolved[j].start == resolved[j].end
		if iInsert != jInsert {
			return !iInsert
		}
		return resolved[i].order > resolved[j].order
	})
	out := text
	for _, edit := range resolved {
		out = out[:edit.start] + edit.text + out[edit.end:]
	}
	return out, nil
}

func resolveEdit(text string, lines []editSourceLine, edit Edit, operation string) ([]resolvedEdit, error) {
	if err := validateEditFields(edit, operation); err != nil {
		return nil, err
	}
	switch operation {
	case "replace_text":
		oldText := NormalizeEditText(edit.OldText)
		locations, err := selectMatches(findContentMatches(text, oldText), edit.Index, edit.AllMatches, "old_text")
		if err != nil {
			return nil, err
		}
		out := make([]resolvedEdit, 0, len(locations))
		for _, loc := range locations {
			out = append(out, resolvedEdit{start: loc.byteStart, end: loc.byteEnd, text: NormalizeEditText(*edit.NewText)})
		}
		return out, nil
	case "replace":
		endLine := edit.EndLine
		if endLine == 0 {
			endLine = edit.Line
		}
		if err := ValidateLineRange(len(lines), edit.Line, endLine); err != nil {
			return nil, err
		}
		start, end := lineDeleteRange(lines, edit.Line, endLine)
		return []resolvedEdit{{start: start, end: end, text: NormalizeEditText(*edit.NewText)}}, nil
	case "insert":
		block, err := normalizeLineBlock(*edit.NewText)
		if err != nil {
			return nil, err
		}
		start, inserted, err := resolveLineInsert(text, lines, edit.Line, block)
		if err != nil {
			return nil, err
		}
		return []resolvedEdit{{start: start, end: start, text: inserted}}, nil
	case "delete":
		endLine := edit.EndLine
		if endLine == 0 {
			endLine = edit.Line
		}
		if err := ValidateLineRange(len(lines), edit.Line, endLine); err != nil {
			return nil, err
		}
		start, end := lineDeleteRange(lines, edit.Line, endLine)
		return []resolvedEdit{{start: start, end: end}}, nil
	case "insert_before", "insert_after", "replace_line", "delete_line":
		anchor := NormalizeEditText(edit.Anchor)
		locations, err := selectMatches(findLineMatches(lines, anchor), edit.Index, edit.AllMatches, "anchor")
		if err != nil {
			return nil, err
		}
		var block string
		if operation != "delete_line" {
			block, err = normalizeLineBlock(*edit.NewText)
			if err != nil {
				return nil, err
			}
		}
		out := make([]resolvedEdit, 0, len(locations))
		for _, loc := range locations {
			line := lines[loc.lineIndex]
			switch operation {
			case "insert_before":
				out = append(out, resolvedEdit{start: line.start, end: line.start, text: block + "\n"})
			case "insert_after":
				if line.fullEnd > line.end {
					out = append(out, resolvedEdit{start: line.fullEnd, end: line.fullEnd, text: block + "\n"})
				} else {
					out = append(out, resolvedEdit{start: line.end, end: line.end, text: "\n" + block})
				}
			case "replace_line":
				out = append(out, resolvedEdit{start: line.start, end: line.end, text: block})
			case "delete_line":
				start, end := lineDeleteRange(lines, loc.lineIndex+1, loc.lineIndex+1)
				out = append(out, resolvedEdit{start: start, end: end})
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported operation %q", edit.Operation)
	}
}

func validateEditFields(edit Edit, operation string) error {
	if operation == "" {
		return fmt.Errorf("operation is required")
	}
	if edit.Index != nil && edit.AllMatches {
		return fmt.Errorf("index and all_matches are mutually exclusive")
	}
	rejectLineFields := func() error {
		if edit.Line != 0 || edit.EndLine != 0 {
			return fmt.Errorf("operation %q does not use line or end_line", operation)
		}
		return nil
	}
	rejectMatchFields := func() error {
		if edit.Index != nil || edit.AllMatches {
			return fmt.Errorf("operation %q does not support index or all_matches", operation)
		}
		return nil
	}
	switch operation {
	case "replace_text":
		if NormalizeEditText(edit.OldText) == "" {
			return fmt.Errorf("old_text is required")
		}
		if edit.NewText == nil {
			return fmt.Errorf("new_text is required")
		}
		if strings.TrimSpace(edit.Anchor) != "" {
			return fmt.Errorf("operation %q uses old_text, not anchor", operation)
		}
		return rejectLineFields()
	case "replace":
		if edit.Line < 1 {
			return fmt.Errorf("line must be >= 1")
		}
		if edit.NewText == nil {
			return fmt.Errorf("new_text is required")
		}
		if edit.OldText != "" || edit.Anchor != "" {
			return fmt.Errorf("operation %q only uses line, optional end_line, and new_text", operation)
		}
		return rejectMatchFields()
	case "insert":
		if edit.Line < 1 {
			return fmt.Errorf("line must be >= 1")
		}
		if edit.EndLine != 0 || edit.OldText != "" || edit.Anchor != "" {
			return fmt.Errorf("operation %q only uses line and new_text", operation)
		}
		if edit.NewText == nil {
			return fmt.Errorf("new_text is required")
		}
		return rejectMatchFields()
	case "delete":
		if edit.Line < 1 {
			return fmt.Errorf("line must be >= 1")
		}
		if edit.NewText != nil || edit.OldText != "" || edit.Anchor != "" {
			return fmt.Errorf("operation %q only uses line and optional end_line", operation)
		}
		return rejectMatchFields()
	case "insert_before", "insert_after", "replace_line", "delete_line":
		anchor := NormalizeEditText(edit.Anchor)
		if anchor == "" {
			return fmt.Errorf("anchor is required")
		}
		if strings.Contains(anchor, "\n") {
			return fmt.Errorf("anchor must be a single-line prefix")
		}
		if edit.OldText != "" {
			return fmt.Errorf("operation %q uses anchor, not old_text", operation)
		}
		if err := rejectLineFields(); err != nil {
			return err
		}
		if operation == "delete_line" {
			if edit.NewText != nil {
				return fmt.Errorf("operation %q does not use new_text", operation)
			}
		} else if edit.NewText == nil {
			return fmt.Errorf("new_text is required")
		}
		return nil
	case "overwrite":
		if edit.NewText == nil {
			return fmt.Errorf("new_text is required")
		}
		if edit.Line != 0 || edit.EndLine != 0 || edit.OldText != "" || edit.Anchor != "" || edit.Index != nil || edit.AllMatches {
			return fmt.Errorf("operation %q only uses new_text", operation)
		}
		return nil
	default:
		return fmt.Errorf("unsupported operation %q", edit.Operation)
	}
}

func scanEditLines(text string) []editSourceLine {
	if text == "" {
		return nil
	}
	lines := make([]editSourceLine, 0, strings.Count(text, "\n")+1)
	for start := 0; start < len(text); {
		rel := strings.IndexByte(text[start:], '\n')
		if rel < 0 {
			lines = append(lines, editSourceLine{start: start, end: len(text), fullEnd: len(text), text: text[start:]})
			break
		}
		end := start + rel
		lines = append(lines, editSourceLine{start: start, end: end, fullEnd: end + 1, text: text[start:end]})
		start = end + 1
	}
	return lines
}

func normalizeLineBlock(text string) (string, error) {
	lines := SplitLines(EnsureTrailingNewline(text))
	if len(lines) == 0 {
		return "", fmt.Errorf("new_text must contain at least one line")
	}
	return strings.Join(lines, "\n"), nil
}

func resolveLineInsert(text string, lines []editSourceLine, line int, block string) (int, string, error) {
	total := len(lines)
	if line < 1 || line > total+1 {
		return 0, "", fmt.Errorf("insert line %d exceeds valid positions 1-%d", line, total+1)
	}
	if total == 0 {
		return 0, block, nil
	}
	if line <= total {
		return lines[line-1].start, block + "\n", nil
	}
	if strings.HasSuffix(text, "\n") {
		return len(text), block + "\n", nil
	}
	return len(text), "\n" + block, nil
}

func lineDeleteRange(lines []editSourceLine, startLine, endLine int) (int, int) {
	start := lines[startLine-1].start
	end := lines[endLine-1].fullEnd
	last := lines[endLine-1]
	if endLine == len(lines) && last.fullEnd == last.end && startLine > 1 {
		start = lines[startLine-2].end
	}
	return start, end
}

func validateResolvedEdits(edits []resolvedEdit) error {
	for i := 0; i < len(edits); i++ {
		for j := i + 1; j < len(edits); j++ {
			a, b := edits[i], edits[j]
			aInsert := a.start == a.end
			bInsert := b.start == b.end
			switch {
			case !aInsert && !bInsert && a.start < b.end && b.start < a.end:
				return fmt.Errorf("edits overlap in original content at byte ranges %d-%d and %d-%d", a.start, a.end, b.start, b.end)
			case aInsert && !bInsert && a.start > b.start && a.start < b.end:
				return fmt.Errorf("insertion at byte %d falls inside edited range %d-%d", a.start, b.start, b.end)
			case bInsert && !aInsert && b.start > a.start && b.start < a.end:
				return fmt.Errorf("insertion at byte %d falls inside edited range %d-%d", b.start, a.start, a.end)
			}
		}
	}
	return nil
}

func EnsureTrailingNewline(text string) string {
	text = NormalizeEditText(text)
	if text == "" || strings.HasSuffix(text, "\n") {
		return text
	}
	return text + "\n"
}

func NormalizeEditText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

func RestoreLineEndings(text, lineEnding string) string {
	if lineEnding == "" || lineEnding == "\n" {
		return text
	}
	return strings.ReplaceAll(text, "\n", lineEnding)
}

func NormalizeGrepContextLines(value int) int {
	if value < 0 {
		return 0
	}
	if value == 0 {
		return 2
	}
	if value > 20 {
		return 20
	}
	return value
}

func NormalizeMaxMatches(value int) int {
	if value <= 0 {
		return 20
	}
	if value > 100 {
		return 100
	}
	return value
}

func NormalizeContextLines(value int) int {
	if value < 0 {
		return 0
	}
	if value == 0 {
		return 3
	}
	if value > 20 {
		return 20
	}
	return value
}

func ValidateLineRange(total, start, end int) error {
	if start <= 0 {
		return fmt.Errorf("line must be >= 1")
	}
	if end < start {
		return fmt.Errorf("end_line must be >= line")
	}
	if total == 0 {
		return fmt.Errorf("file has no lines")
	}
	if start > total || end > total {
		return fmt.Errorf("line range %d-%d exceeds total lines %d", start, end, total)
	}
	return nil
}

func SplitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text == "" {
		return nil
	}
	if strings.HasSuffix(text, "\n") {
		text = strings.TrimSuffix(text, "\n")
	}
	if text == "" {
		return []string{""}
	}
	return strings.Split(text, "\n")
}

func DetectLineEnding(text string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	if strings.Contains(text, "\r") {
		return "\r"
	}
	return "\n"
}

func LooksBinary(data []byte) bool {
	limit := len(data)
	if limit > 4096 {
		limit = 4096
	}
	return bytes.Contains(data[:limit], []byte{0})
}

func ContentRevision(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:contentRevisionBytes])
}

type DiffHunk struct {
	ops      []diffOp
	oldStart int
	newStart int
}

func UnifiedDiff(path string, oldLines, newLines []string, contextLines int) string {
	if diffTooLarge(oldLines, newLines) {
		return fmt.Sprintf("--- %s\n+++ %s\n# diff omitted: too many line comparisons (%d x %d)\n", path, path, len(oldLines), len(newLines))
	}
	ops := diffLines(oldLines, newLines)
	hunks := buildDiffHunks(ops, contextLines)
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n", path)
	fmt.Fprintf(&b, "+++ %s\n", path)
	for _, hunk := range hunks {
		oldCount, newCount := diffHunkCounts(hunk.ops)
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", hunk.oldStart, oldCount, hunk.newStart, newCount)
		for _, op := range hunk.ops {
			switch op.kind {
			case diffEqual:
				fmt.Fprintf(&b, " %s\n", op.text)
			case diffDelete:
				fmt.Fprintf(&b, "-%s\n", op.text)
			case diffInsert:
				fmt.Fprintf(&b, "+%s\n", op.text)
			}
		}
	}
	return b.String()
}

func diffTooLarge(oldLines, newLines []string) bool {
	if len(oldLines) == 0 || len(newLines) == 0 {
		return false
	}
	return len(oldLines) > maxDiffCells/len(newLines)
}

type diffKind int

const (
	diffEqual diffKind = iota
	diffDelete
	diffInsert
)

type diffOp struct {
	kind diffKind
	text string
}

func diffLines(a, b []string) []diffOp {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	ops := []diffOp{}
	for i, j := 0, 0; i < n || j < m; {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, diffOp{kind: diffEqual, text: a[i]})
			i++
			j++
		case j >= m || i < n && dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{kind: diffDelete, text: a[i]})
			i++
		default:
			ops = append(ops, diffOp{kind: diffInsert, text: b[j]})
			j++
		}
	}
	return ops
}

func buildDiffHunks(ops []diffOp, contextLines int) []DiffHunk {
	firstChange, lastChange := -1, -1
	for i, op := range ops {
		if op.kind != diffEqual {
			if firstChange == -1 {
				firstChange = i
			}
			lastChange = i
		}
	}
	if firstChange == -1 {
		return nil
	}
	var ranges [][2]int
	start := firstChange
	last := firstChange
	for i := firstChange + 1; i <= lastChange; i++ {
		if ops[i].kind == diffEqual {
			continue
		}
		if equalOpsBetween(ops, last+1, i) > contextLines*2 {
			ranges = append(ranges, [2]int{start, last})
			start = i
		}
		last = i
	}
	ranges = append(ranges, [2]int{start, last})
	hunks := make([]DiffHunk, 0, len(ranges))
	for _, r := range ranges {
		hunks = append(hunks, trimDiffContextRange(ops, r[0], r[1], contextLines))
	}
	return hunks
}

func equalOpsBetween(ops []diffOp, start, end int) int {
	count := 0
	for i := start; i < end; i++ {
		if ops[i].kind == diffEqual {
			count++
		}
	}
	return count
}

func trimDiffContextRange(ops []diffOp, first, last, contextLines int) DiffHunk {
	start := first - contextLines
	if start < 0 {
		start = 0
	}
	end := last + contextLines + 1
	if end > len(ops) {
		end = len(ops)
	}
	oldStart, newStart := 1, 1
	for _, op := range ops[:start] {
		switch op.kind {
		case diffEqual:
			oldStart++
			newStart++
		case diffDelete:
			oldStart++
		case diffInsert:
			newStart++
		}
	}
	return DiffHunk{ops: ops[start:end], oldStart: oldStart, newStart: newStart}
}

func diffHunkCounts(ops []diffOp) (int, int) {
	oldCount, newCount := 0, 0
	for _, op := range ops {
		switch op.kind {
		case diffEqual:
			oldCount++
			newCount++
		case diffDelete:
			oldCount++
		case diffInsert:
			newCount++
		}
	}
	return oldCount, newCount
}
