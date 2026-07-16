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
	matchModeContent     = "content"
	matchModeLine        = "line"
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
	Operation       string     `json:"operation"`
	StartLine       int        `json:"start_line"`
	EndLine         LineNumber `json:"end_line"`
	Content         string     `json:"content"`
	ExpectedContent *string    `json:"expected_content"`
	OldContent      string     `json:"old_content"`
	Anchor          string     `json:"anchor"`
	MatchMode       string     `json:"match_mode"`
	Index           *int       `json:"index"`
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
		"operation":        map[string]any{"type": "string", "description": "编辑操作：replace、delete、insert_line_before、insert_line_after、prepend、append、replace_match、delete_match、insert_before_match、insert_after_match。edits 按顺序应用；行号操作的 start_line/end_line 均引用编辑前文件的原始行号，工具会自动补偿同一批内前序行号编辑造成的漂移。"},
		"start_line":       map[string]any{"type": "integer", "description": "行号操作的起始行号，1-based，引用编辑前文件的原始行号。"},
		"end_line":         map[string]any{"type": "string", "description": "行号 replace/delete 的结束行号，1-based 且包含该行，引用编辑前文件的原始行号；也可传 end；默认等于 start_line。"},
		"content":          map[string]any{"type": "string", "description": "replace/insert 写入的文本；delete/delete_match 忽略此字段。insert_line_*、prepend、append 及 line 模式 insert_*_match 按整行插入并自动补换行，无需自加换行符；content 模式 insert_*_match 为字面插入，不自动加换行符。"},
		"expected_content": map[string]any{"type": "string", "description": "replace/delete 前校验目标行范围原始文本；换行符按 \\n 规范化比较，用于防止行号漂移误改；不需要校验时请省略该字段，不要传空字符串。"},
		"old_content":      map[string]any{"type": "string", "description": "replace_match/delete_match 的匹配文本。match_mode=content（默认）时为精确子串，写多少匹配/替换多少；match_mode=line 时为单行前缀，匹配并操作整行。"},
		"anchor":           map[string]any{"type": "string", "description": "insert_before_match/insert_after_match 的匹配文本，语义同 old_content，受 match_mode 控制。"},
		"match_mode":       map[string]any{"type": "string", "enum": []string{"content", "line"}, "description": "*_match 的匹配方式。content（默认）：精确子串，写多少替换多少，insert 为字面插入不自动加换行符。line：单行前缀匹配整行，容忍行首缩进，needle 不得含换行；操作整行，insert 按整行插入自动补换行，content 可多行展开。仅对 *_match 操作有效。*_match 后不要在同一批继续使用行号操作；如需继续按行号编辑，请拆成下一次 edit_file 调用。"},
		"index":            map[string]any{"type": "integer", "description": "当 old_content/anchor 匹配到多处时，用 index 选择第几处，1-based。默认不填：唯一匹配时直接命中，多处匹配时报错并列出所有匹配位置供你更精准匹配或传入 index。仅对 *_match 操作有效。"},
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

func ApplyEdits(text string, edits []Edit) (string, error) {
	if len(edits) == 0 {
		return "", fmt.Errorf("edits is required")
	}
	current := NormalizeEditText(text)
	mapper := newLineEditMapper(len(SplitLines(current)))
	for i, edit := range edits {
		mappedEdit, err := mapper.mapEdit(edit)
		if err != nil {
			return "", fmt.Errorf("edit %d: %w", i+1, err)
		}
		next, err := applyEdit(current, mappedEdit)
		if err != nil {
			return "", fmt.Errorf("edit %d: %w", i+1, err)
		}
		if err := mapper.recordEdit(edit); err != nil {
			return "", fmt.Errorf("edit %d: %w", i+1, err)
		}
		current = next
	}
	return current, nil
}

type lineEditMapper struct {
	originalLineCount int
	deltas            []lineDelta
	blocked           []lineRange
	insertions        []int
	afterLineInserts  map[int]int
	seenMatchEdit     bool
}

type lineDelta struct {
	threshold int
	delta     int
}

type lineRange struct {
	start int
	end   int
}

func newLineEditMapper(originalLineCount int) *lineEditMapper {
	return &lineEditMapper{originalLineCount: originalLineCount, afterLineInserts: map[int]int{}}
}

func (m *lineEditMapper) mapEdit(edit Edit) (Edit, error) {
	operation := strings.ToLower(strings.TrimSpace(edit.Operation))
	if usesOriginalLineNumbers(operation) && m.seenMatchEdit {
		return Edit{}, fmt.Errorf("line-number operation %q cannot follow a match operation in the same batch; split it into a separate edit_file call or put line-number edits before match edits", edit.Operation)
	}
	mapped := edit
	start, end := m.originalRange(edit, operation)
	switch operation {
	case "replace", "delete":
		if err := m.ensureRangeAvailable(start, end); err != nil {
			return Edit{}, err
		}
		if err := m.ensureNoInsertedContentInside(start, end); err != nil {
			return Edit{}, err
		}
		mapped.StartLine = m.mapLine(start)
		mapped.EndLine = LineNumber{Value: m.mapLine(end)}
	case "insert_line_before":
		if err := m.ensureRangeAvailable(start, start); err != nil {
			return Edit{}, err
		}
		mapped.StartLine = m.mapLine(start)
	case "insert_line_after":
		if err := m.ensureRangeAvailable(start, start); err != nil {
			return Edit{}, err
		}
		mapped.StartLine = m.mapInsertAfterLine(start)
	}
	return mapped, nil
}

func (m *lineEditMapper) recordEdit(edit Edit) error {
	operation := strings.ToLower(strings.TrimSpace(edit.Operation))
	start, end := m.originalRange(edit, operation)
	switch operation {
	case "replace":
		oldCount := end - start + 1
		newCount := editLineCount(edit.Content)
		m.blocked = append(m.blocked, lineRange{start: start, end: end})
		m.addDelta(end+1, newCount-oldCount)
	case "delete":
		oldCount := end - start + 1
		m.blocked = append(m.blocked, lineRange{start: start, end: end})
		m.addDelta(end+1, -oldCount)
	case "insert_line_before":
		m.recordInsertion(start, editLineCount(edit.Content))
	case "insert_line_after":
		count := editLineCount(edit.Content)
		m.recordInsertion(start+1, count)
		m.afterLineInserts[start] += count
	case "prepend":
		m.recordInsertion(1, editLineCount(edit.Content))
	case "append":
		m.recordInsertion(m.originalLineCount+1, editLineCount(edit.Content))
	case "replace_match", "delete_match", "insert_before_match", "insert_after_match":
		m.seenMatchEdit = true
	}
	return nil
}

func (m *lineEditMapper) originalRange(edit Edit, operation string) (int, int) {
	start := edit.StartLine
	end := edit.EndLine.Value
	if edit.EndLine.End {
		end = m.originalLineCount
	} else if end == 0 || operation == "insert_line_before" || operation == "insert_line_after" {
		end = start
	}
	return start, end
}

func (m *lineEditMapper) mapLine(line int) int {
	mapped := line
	for _, delta := range m.deltas {
		if line >= delta.threshold {
			mapped += delta.delta
		}
	}
	return mapped
}

func (m *lineEditMapper) mapInsertAfterLine(line int) int {
	return m.mapLine(line) + m.afterLineInserts[line]
}

func (m *lineEditMapper) recordInsertion(position, count int) {
	if count == 0 {
		return
	}
	m.insertions = append(m.insertions, position)
	m.addDelta(position, count)
}

func (m *lineEditMapper) addDelta(threshold, delta int) {
	if delta == 0 {
		return
	}
	m.deltas = append(m.deltas, lineDelta{threshold: threshold, delta: delta})
}

func (m *lineEditMapper) ensureRangeAvailable(start, end int) error {
	if start <= 0 || end < start {
		return nil
	}
	for _, blocked := range m.blocked {
		if start <= blocked.end && end >= blocked.start {
			return fmt.Errorf("original lines %s were already modified by a previous line edit", formatOriginalRange(start, end))
		}
	}
	return nil
}

func (m *lineEditMapper) ensureNoInsertedContentInside(start, end int) error {
	if start <= 0 || end <= start {
		return nil
	}
	for _, position := range m.insertions {
		if position > start && position <= end {
			return fmt.Errorf("original lines %s contain content inserted by a previous line edit; split the range edit into separate edits or put it before the insertion", formatOriginalRange(start, end))
		}
	}
	return nil
}

func usesOriginalLineNumbers(operation string) bool {
	switch operation {
	case "replace", "delete", "insert_line_before", "insert_line_after":
		return true
	default:
		return false
	}
}

func editLineCount(content string) int {
	return len(SplitLines(content))
}

func formatOriginalRange(start, end int) string {
	if start == end {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d-%d", start, end)
}

func applyEdit(text string, edit Edit) (string, error) {
	operation := strings.ToLower(strings.TrimSpace(edit.Operation))
	if operation == "" {
		return "", fmt.Errorf("operation is required")
	}
	switch operation {
	case "replace", "delete", "insert_line_before", "insert_line_after", "prepend", "append":
		if strings.TrimSpace(edit.Anchor) != "" {
			return "", fmt.Errorf("operation %q uses line numbers, not anchor; did you mean replace_match, delete_match, insert_before_match or insert_after_match?", edit.Operation)
		}
		if strings.TrimSpace(edit.MatchMode) != "" && !isMatchMode(edit.MatchMode) {
			return "", fmt.Errorf("operation %q does not support match_mode", edit.Operation)
		}
		if edit.Index != nil {
			return "", fmt.Errorf("operation %q does not support index", edit.Operation)
		}
		return applyLineEdit(text, edit)
	case "replace_match", "delete_match", "insert_before_match", "insert_after_match":
		return applyMatchEdit(text, edit)
	default:
		return "", fmt.Errorf("unsupported operation %q", edit.Operation)
	}
}

func applyMatchEdit(text string, edit Edit) (string, error) {
	operation := strings.ToLower(strings.TrimSpace(edit.Operation))
	mode := strings.ToLower(strings.TrimSpace(edit.MatchMode))
	if mode == "" {
		mode = matchModeContent
	}
	if mode != matchModeContent && mode != matchModeLine {
		return "", fmt.Errorf("match_mode must be %q or %q", matchModeContent, matchModeLine)
	}
	if mode == matchModeLine {
		return applyLineMatchEdit(text, edit, operation)
	}
	return applyContentMatchEdit(text, edit, operation)
}

func applyContentMatchEdit(text string, edit Edit, operation string) (string, error) {
	needle, label := matchNeedle(edit, operation)
	matches := findContentMatches(text, needle)
	loc, err := selectMatch(matches, edit.Index, label)
	if err != nil {
		return "", err
	}
	switch operation {
	case "replace_match":
		return text[:loc.byteStart] + NormalizeEditText(edit.Content) + text[loc.byteEnd:], nil
	case "delete_match":
		return text[:loc.byteStart] + text[loc.byteEnd:], nil
	case "insert_before_match", "insert_after_match":
		index := loc.byteStart
		if operation == "insert_after_match" {
			index = loc.byteEnd
		}
		return text[:index] + NormalizeEditText(edit.Content) + text[index:], nil
	}
	return "", fmt.Errorf("unsupported operation %q", operation)
}

func applyLineMatchEdit(text string, edit Edit, operation string) (string, error) {
	needle, label := matchNeedle(edit, operation)
	needleNorm := NormalizeEditText(needle)
	if needleNorm == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	if strings.Contains(needleNorm, "\n") {
		return "", fmt.Errorf("%s in line mode must be a single-line prefix; for multi-line matches use content mode", label)
	}
	lines := SplitLines(text)
	endsNewline := strings.HasSuffix(text, "\n")
	matches := findLineMatches(lines, needleNorm)
	loc, err := selectMatch(matches, edit.Index, label)
	if err != nil {
		return "", err
	}
	targetLine := loc.startLine
	switch operation {
	case "replace_match":
		contentLines := SplitLines(edit.Content)
		out := append([]string{}, lines...)
		out = ReplaceLines(out, targetLine, targetLine, contentLines)
		return JoinLines(out, "\n", endsNewline), nil
	case "delete_match":
		out := append([]string{}, lines...)
		out = ReplaceLines(out, targetLine, targetLine, nil)
		return JoinLines(out, "\n", endsNewline), nil
	case "insert_before_match", "insert_after_match":
		lineContentLines := SplitLines(EnsureTrailingNewline(edit.Content))
		out := append([]string{}, lines...)
		index := targetLine - 1
		if operation == "insert_after_match" {
			index = targetLine
		}
		out = append(out[:index], append(lineContentLines, out[index:]...)...)
		return JoinLines(out, "\n", endsNewline || len(lineContentLines) > 0), nil
	}
	return "", fmt.Errorf("unsupported operation %q", operation)
}

func isMatchMode(mode string) bool {
	mode = strings.ToLower(strings.TrimSpace(mode))
	return mode == matchModeContent || mode == matchModeLine
}

func matchNeedle(edit Edit, operation string) (string, string) {
	if operation == "insert_before_match" || operation == "insert_after_match" {
		return edit.Anchor, "anchor"
	}
	return edit.OldContent, "old_content"
}

type matchLocation struct {
	byteStart int
	byteEnd   int
	startLine int
	endLine   int
	preview   string
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

func findLineMatches(lines []string, needle string) []matchLocation {
	var locations []matchLocation
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), needle) {
			locations = append(locations, matchLocation{
				startLine: i + 1,
				endLine:   i + 1,
				preview:   truncateLine(strings.TrimLeft(line, " \t")),
			})
		}
	}
	return locations
}

func selectMatch(locations []matchLocation, index *int, label string) (matchLocation, error) {
	n := len(locations)
	if n == 0 {
		return matchLocation{}, fmt.Errorf("%s not found", label)
	}
	if n == 1 {
		return locations[0], nil
	}
	if index == nil {
		return matchLocation{}, fmt.Errorf("%s matched %d locations; provide longer %s or pass index:\n%s", label, n, label, formatMatchLocations(locations))
	}
	i := *index
	if i < 1 || i > n {
		return matchLocation{}, fmt.Errorf("index %d out of range: %d %s found, use 1-%d", i, n, label, n)
	}
	return locations[i-1], nil
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

func applyLineEdit(text string, edit Edit) (string, error) {
	lines := SplitLines(text)
	endsNewline := strings.HasSuffix(text, "\n")
	operation := strings.ToLower(strings.TrimSpace(edit.Operation))
	start := edit.StartLine
	end := edit.EndLine.Value
	if edit.EndLine.End {
		end = len(lines)
	} else if end == 0 {
		end = start
	}
	contentLines := SplitLines(edit.Content)
	lineContentLines := SplitLines(EnsureTrailingNewline(edit.Content))
	out := append([]string{}, lines...)
	switch operation {
	case "replace":
		if err := ValidateLineRange(len(lines), start, end); err != nil {
			return "", err
		}
		if err := validateExpectedContent(lines, start, end, edit.ExpectedContent); err != nil {
			return "", err
		}
		out = ReplaceLines(out, start, end, contentLines)
	case "delete":
		if err := ValidateLineRange(len(lines), start, end); err != nil {
			return "", err
		}
		if err := validateExpectedContent(lines, start, end, edit.ExpectedContent); err != nil {
			return "", err
		}
		out = ReplaceLines(out, start, end, nil)
	case "insert_line_before", "insert_line_after":
		if err := ValidateInsertLine(len(lines), start); err != nil {
			return "", err
		}
		index := start - 1
		if operation == "insert_line_after" {
			index = start
		}
		out = append(out[:index], append(lineContentLines, out[index:]...)...)
		endsNewline = true
	case "prepend":
		out = append(lineContentLines, out...)
		endsNewline = true
	case "append":
		out = append(out, lineContentLines...)
		endsNewline = true
	default:
		return "", fmt.Errorf("unsupported operation %q", edit.Operation)
	}
	return JoinLines(out, "\n", endsNewline), nil
}

func validateExpectedContent(lines []string, start, end int, expected *string) error {
	if expected == nil || *expected == "" {
		return nil
	}
	actual := strings.Join(lines[start-1:end], "\n")
	want := strings.TrimSuffix(NormalizeEditText(*expected), "\n")
	if actual != want {
		return fmt.Errorf("target content mismatch at lines %d-%d", start, end)
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
		return fmt.Errorf("start_line must be >= 1")
	}
	if end < start {
		return fmt.Errorf("end_line must be >= start_line")
	}
	if total == 0 {
		return fmt.Errorf("file has no lines")
	}
	if start > total || end > total {
		return fmt.Errorf("line range %d-%d exceeds total lines %d", start, end, total)
	}
	return nil
}

func ValidateInsertLine(total, line int) error {
	if total == 0 {
		if line == 1 {
			return nil
		}
		return fmt.Errorf("empty file only supports insert at line 1")
	}
	if line < 1 || line > total {
		return fmt.Errorf("insert line %d exceeds total lines %d", line, total)
	}
	return nil
}

func ReplaceLines(lines []string, start, end int, replacement []string) []string {
	out := make([]string, 0, len(lines)-(end-start+1)+len(replacement))
	out = append(out, lines[:start-1]...)
	out = append(out, replacement...)
	out = append(out, lines[end:]...)
	return out
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

func JoinLines(lines []string, lineEnding string, endsNewline bool) string {
	if lineEnding == "" {
		lineEnding = "\n"
	}
	text := strings.Join(lines, lineEnding)
	if endsNewline && len(lines) > 0 {
		text += lineEnding
	}
	return text
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
