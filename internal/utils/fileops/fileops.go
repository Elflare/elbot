package fileops

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
)

const (
	DefaultReadLineLimit = 200
	MaxFileSize          = 2 * 1024 * 1024
	maxDiffCells         = 2_000_000
)

type LineNumber struct {
	Value int
	End   bool
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
	Operation       string
	StartLine       int
	EndLine         LineNumber
	Content         string
	ExpectedContent *string
	OldContent      string
	Anchor          string
}

type EditResult struct {
	DryRun       bool
	Path         string
	Created      bool
	Encoding     string
	SHA256Before string
	SHA256After  string
	Diff         string
	NewBytes     []byte
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

func ReadOrCreateFile(path, requestedEncoding string, create bool, hasExpectedSHA bool) (File, bool, error) {
	file, err := ReadFile(path, requestedEncoding)
	if err == nil {
		return file, false, nil
	}
	if !create || hasExpectedSHA || !errors.Is(err, os.ErrNotExist) {
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

func EditFile(path, requestedEncoding, expectedSHA string, create, dryRun bool, contextLines int, edits []Edit) (EditResult, error) {
	file, created, err := ReadOrCreateFile(path, requestedEncoding, create, strings.TrimSpace(expectedSHA) != "")
	if err != nil {
		return EditResult{}, err
	}
	if expected := strings.TrimSpace(expectedSHA); expected != "" && !strings.EqualFold(expected, SHA256Hex(file.Bytes)) {
		return EditResult{}, fmt.Errorf("file sha256 mismatch: current %s", SHA256Hex(file.Bytes))
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
		DryRun:       dryRun,
		Path:         file.Path,
		Created:      created,
		Encoding:     file.Encoding,
		SHA256Before: SHA256Hex(file.Bytes),
		SHA256After:  SHA256Hex(newBytes),
		Diff:         UnifiedDiff(file.Path, SplitLines(oldText), SplitLines(newText), contextLines),
		NewBytes:     newBytes,
	}
	if dryRun {
		return result, nil
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
	for i, edit := range edits {
		next, err := applyEdit(current, edit)
		if err != nil {
			return "", fmt.Errorf("edit %d: %w", i+1, err)
		}
		current = next
	}
	return current, nil
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
		return applyLineEdit(text, edit)
	case "replace_match":
		start, end, err := findUniqueMatch(text, edit.OldContent, "old_content")
		if err != nil {
			return "", err
		}
		return text[:start] + NormalizeEditText(edit.Content) + text[end:], nil
	case "delete_match":
		start, end, err := findUniqueMatch(text, edit.OldContent, "old_content")
		if err != nil {
			return "", err
		}
		return text[:start] + text[end:], nil
	case "insert_before_match", "insert_after_match":
		start, end, err := findUniqueMatch(text, edit.Anchor, "anchor")
		if err != nil {
			return "", err
		}
		index := start
		if operation == "insert_after_match" {
			index = end
		}
		return text[:index] + NormalizeEditText(edit.Content) + text[index:], nil
	default:
		return "", fmt.Errorf("unsupported operation %q", edit.Operation)
	}
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

func findUniqueMatch(text, rawNeedle, label string) (int, int, error) {
	needle := NormalizeEditText(rawNeedle)
	if needle == "" {
		return 0, 0, fmt.Errorf("%s is required", label)
	}
	first := strings.Index(text, needle)
	if first < 0 {
		return 0, 0, fmt.Errorf("%s not found", label)
	}
	if next := strings.Index(text[first+len(needle):], needle); next >= 0 {
		return 0, 0, fmt.Errorf("%s matched multiple locations; provide longer %s", label, label)
	}
	return first, first + len(needle), nil
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

func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
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
