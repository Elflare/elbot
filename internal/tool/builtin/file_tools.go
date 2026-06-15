package builtin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"elbot/internal/llm"
	"elbot/internal/tool"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
)

const (
	defaultReadLineLimit = 200
	maxFileToolSize      = 2 * 1024 * 1024
)

type ReadFileTool struct{}

type EditFileTool struct{}

type lineNumber struct {
	Value int
	End   bool
}

func (n *lineNumber) UnmarshalJSON(data []byte) error {
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
		return fmt.Errorf("line number string must be \"end\"")
	}
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("line number must be integer or \"end\"")
	}
	n.Value = value
	n.End = false
	return nil
}

type readFileArgs struct {
	Path      string     `json:"path"`
	Encoding  string     `json:"encoding"`
	StartLine int        `json:"start_line"`
	EndLine   lineNumber `json:"end_line"`
}

type editFileArgs struct {
	Path           string     `json:"path"`
	Encoding       string     `json:"encoding"`
	Operation      string     `json:"operation"`
	StartLine      int        `json:"start_line"`
	EndLine        lineNumber `json:"end_line"`
	Content        string     `json:"content"`
	ExpectedSHA256 string     `json:"expected_sha256"`
}

type decodedFile struct {
	Path        string
	Bytes       []byte
	Text        string
	Encoding    string
	BOM         []byte
	LineEnding  string
	EndsNewline bool
}

func NewReadFileTool() ReadFileTool {
	return ReadFileTool{}
}

func (ReadFileTool) Name() string {
	return "read_file"
}

func (t ReadFileTool) Info() tool.Info {
	return readFileBuilder().BuildInfo()
}

func (t ReadFileTool) Schema() llm.ToolSchema {
	return readFileBuilder().BuildSchema()
}

func readFileBuilder() *tool.Builder {
	return tool.NewBuilder("read_file").
		Description("读取文本文件并返回带行号的内容；编辑前应先用它确认行号和文件哈希。").
		Risk(tool.RiskLow).
		Tags("files").
		String("path", "要读取的文件路径，可为相对路径或绝对路径。", tool.Required()).
		String("encoding", "文本编码，默认 auto；可选 utf-8、utf-8-bom、utf-16le、utf-16be、gbk、gb18030、big5、shift_jis 等。").
		Integer("start_line", "起始行号，1-based；默认 1。").
		String("end_line", "结束行号，1-based 且包含该行；也可传 end 表示文件末尾；默认最多返回 200 行。")
}

func (ReadFileTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args readFileArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse read_file arguments: %w", err)
		}
	}
	path, err := resolveFileToolPath(ctx, args.Path, false)
	if err != nil {
		return nil, err
	}
	file, err := readDecodedFile(path, args.Encoding)
	if err != nil {
		return nil, err
	}
	lines := splitLines(file.Text)
	start, end, truncated, err := normalizeReadRange(len(lines), args.StartLine, args.EndLine)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "file: %s\n", file.Path)
	fmt.Fprintf(&b, "encoding: %s\n", file.Encoding)
	fmt.Fprintf(&b, "sha256: %s\n", sha256Hex(file.Bytes))
	fmt.Fprintf(&b, "lines: %d-%d/%d\n", start, end, len(lines))
	fmt.Fprintf(&b, "truncated: %t\n", truncated)
	b.WriteString("content:\n")
	width := len(fmt.Sprintf("%d", end))
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%*d | %s\n", width, i, lines[i-1])
	}
	return &tool.Result{Content: b.String()}, nil
}

func NewEditFileTool() EditFileTool {
	return EditFileTool{}
}

func (EditFileTool) Name() string {
	return "edit_file"
}

func (t EditFileTool) Info() tool.Info {
	return editFileBuilder().BuildInfo()
}

func (t EditFileTool) Schema() llm.ToolSchema {
	return editFileBuilder().BuildSchema()
}

func editFileBuilder() *tool.Builder {
	return tool.NewBuilder("edit_file").
		Description("按行编辑文本文件，支持替换、插入和删除；成功后返回 unified diff。编辑前应先用 read_file 确认行号。").
		Risk(tool.RiskHigh).
		Tags("files").
		String("path", "要编辑的文件路径，可为相对路径或绝对路径；cron 后台只能编辑 sandbox 内文件。", tool.Required()).
		String("encoding", "文本编码，默认 auto；非 UTF-8 文件应显式传入 gb18030、gbk、big5、shift_jis 等。").
		String("operation", "编辑操作：replace、insert_before、insert_after、delete。", tool.Required()).
		Integer("start_line", "起始行号，1-based。replace/delete 需要；insert_before/insert_after 表示插入位置。", tool.Required()).
		String("end_line", "结束行号，1-based 且包含该行；也可传 end 表示文件末尾；replace/delete 默认等于 start_line。").
		String("content", "replace/insert 写入的文本；delete 会忽略该字段。").
		String("expected_sha256", "可选，编辑前文件 sha256；若当前文件不一致则拒绝编辑。")
}

func (EditFileTool) AssessRisk(ctx context.Context, req tool.CallRequest) (tool.RiskAssessment, error) {
	var args editFileArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return tool.RiskAssessment{}, fmt.Errorf("parse edit_file arguments: %w", err)
		}
	}
	if _, err := resolveFileToolPath(ctx, args.Path, true); err != nil {
		return tool.RiskAssessment{}, err
	}
	if sandbox, ok := tool.SandboxContextFromContext(ctx); ok && sandbox.CronBackground {
		return tool.RiskAssessment{Level: tool.RiskMedium, Reasons: []string{"cron 后台文件编辑限制在 sandbox 内"}}, nil
	}
	return tool.RiskAssessment{Level: tool.RiskHigh, Reasons: []string{"文件内容写入操作需要确认"}}, nil
}

func (EditFileTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args editFileArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse edit_file arguments: %w", err)
		}
	}
	path, err := resolveFileToolPath(ctx, args.Path, true)
	if err != nil {
		return nil, err
	}
	file, err := readDecodedFile(path, args.Encoding)
	if err != nil {
		return nil, err
	}
	if expected := strings.TrimSpace(args.ExpectedSHA256); expected != "" && !strings.EqualFold(expected, sha256Hex(file.Bytes)) {
		return nil, fmt.Errorf("file sha256 mismatch: current %s", sha256Hex(file.Bytes))
	}
	oldLines := splitLines(file.Text)
	newLines, err := applyLineEdit(oldLines, args)
	if err != nil {
		return nil, err
	}
	newText := joinLines(newLines, file.LineEnding, file.EndsNewline)
	if newText == file.Text {
		return nil, fmt.Errorf("edit produced no changes")
	}
	newBytes, err := encodeText(newText, file.Encoding, file.BOM)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	if err := os.WriteFile(path, newBytes, info.Mode()); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}
	diff := unifiedDiff(file.Path, oldLines, newLines, file.LineEnding)
	content := fmt.Sprintf("edited: %s\nencoding: %s\nsha256_before: %s\nsha256_after: %s\ndiff:\n%s", file.Path, file.Encoding, sha256Hex(file.Bytes), sha256Hex(newBytes), diff)
	return &tool.Result{Content: content}, nil
}

func resolveFileToolPath(ctx context.Context, rawPath string, forWrite bool) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", fmt.Errorf("path is required")
	}
	if sandbox, ok := tool.SandboxContextFromContext(ctx); ok && sandbox.CronBackground {
		root := strings.TrimSpace(sandbox.Dir)
		if root == "" {
			root = strings.TrimSpace(sandbox.Root)
		}
		if root == "" {
			return "", fmt.Errorf("cron sandbox is not configured")
		}
		return resolveInsideRoot(rawPath, root)
	}
	path := filepath.Clean(rawPath)
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve path: %w", err)
		}
		path = abs
	}
	if forWrite {
		if info, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("stat file: %w", err)
		} else if info.IsDir() {
			return "", fmt.Errorf("path is a directory")
		}
	}
	return path, nil
}

func resolveInsideRoot(rawPath, root string) (string, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		abs, err := filepath.Abs(root)
		if err != nil {
			return "", fmt.Errorf("resolve sandbox root: %w", err)
		}
		root = abs
	}
	candidate := filepath.Clean(rawPath)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	rel, err := filepath.Rel(root, candidateAbs)
	if err != nil {
		return "", fmt.Errorf("check sandbox path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes cron sandbox")
	}
	return candidateAbs, nil
}

func readDecodedFile(path, requestedEncoding string) (decodedFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return decodedFile{}, fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() {
		return decodedFile{}, fmt.Errorf("path is a directory")
	}
	if info.Size() > maxFileToolSize {
		return decodedFile{}, fmt.Errorf("file too large: %d bytes exceeds %d", info.Size(), maxFileToolSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return decodedFile{}, fmt.Errorf("read file: %w", err)
	}
	if looksBinary(data) {
		return decodedFile{}, fmt.Errorf("file appears to be binary")
	}
	text, encName, bom, err := decodeBytes(data, requestedEncoding)
	if err != nil {
		return decodedFile{}, err
	}
	return decodedFile{Path: path, Bytes: data, Text: text, Encoding: encName, BOM: bom, LineEnding: detectLineEnding(text), EndsNewline: strings.HasSuffix(text, "\n")}, nil
}

func decodeBytes(data []byte, requested string) (string, string, []byte, error) {
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
	enc, err := lookupEncoding(name)
	if err != nil {
		return "", "", nil, err
	}
	decoded, _, err := transform.Bytes(enc.NewDecoder(), data)
	if err != nil {
		return "", "", nil, fmt.Errorf("decode %s: %w", name, err)
	}
	return string(decoded), name, bom, nil
}

func encodeText(text, name string, bom []byte) ([]byte, error) {
	if name == "utf-8" {
		return []byte(text), nil
	}
	if name == "utf-8-bom" {
		return append(append([]byte{}, bom...), []byte(text)...), nil
	}
	enc, err := lookupEncoding(name)
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

func lookupEncoding(name string) (encoding.Encoding, error) {
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

func normalizeReadRange(total, start int, endLine lineNumber) (int, int, bool, error) {
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
		end = start + defaultReadLineLimit - 1
		truncated = end < total
	}
	if end > total {
		end = total
	}
	if end < start {
		return 0, 0, false, fmt.Errorf("end_line must be >= start_line")
	}
	if !endLine.End && end-start+1 > defaultReadLineLimit {
		end = start + defaultReadLineLimit - 1
		truncated = true
	}
	return start, end, truncated, nil
}

func applyLineEdit(lines []string, args editFileArgs) ([]string, error) {
	operation := strings.ToLower(strings.TrimSpace(args.Operation))
	if operation == "" {
		return nil, fmt.Errorf("operation is required")
	}
	start := args.StartLine
	end := args.EndLine.Value
	if args.EndLine.End {
		end = len(lines)
	} else if end == 0 {
		end = start
	}
	contentLines := splitLines(args.Content)
	out := append([]string{}, lines...)
	switch operation {
	case "replace":
		if err := validateLineRange(len(lines), start, end); err != nil {
			return nil, err
		}
		return replaceLines(out, start, end, contentLines), nil
	case "delete":
		if err := validateLineRange(len(lines), start, end); err != nil {
			return nil, err
		}
		return replaceLines(out, start, end, nil), nil
	case "insert_before", "insert_after":
		if err := validateInsertLine(len(lines), start); err != nil {
			return nil, err
		}
		index := start - 1
		if operation == "insert_after" {
			index = start
		}
		out = append(out[:index], append(contentLines, out[index:]...)...)
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported operation %q", args.Operation)
	}
}

func validateLineRange(total, start, end int) error {
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

func validateInsertLine(total, line int) error {
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

func replaceLines(lines []string, start, end int, replacement []string) []string {
	out := make([]string, 0, len(lines)-(end-start+1)+len(replacement))
	out = append(out, lines[:start-1]...)
	out = append(out, replacement...)
	out = append(out, lines[end:]...)
	return out
}

func splitLines(text string) []string {
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

func joinLines(lines []string, lineEnding string, endsNewline bool) string {
	if lineEnding == "" {
		lineEnding = "\n"
	}
	text := strings.Join(lines, lineEnding)
	if endsNewline && len(lines) > 0 {
		text += lineEnding
	}
	return text
}

func detectLineEnding(text string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	if strings.Contains(text, "\r") {
		return "\r"
	}
	return "\n"
}

func looksBinary(data []byte) bool {
	limit := len(data)
	if limit > 4096 {
		limit = 4096
	}
	return bytes.Contains(data[:limit], []byte{0})
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func unifiedDiff(path string, oldLines, newLines []string, lineEnding string) string {
	ops := diffLines(oldLines, newLines)
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n", path)
	fmt.Fprintf(&b, "+++ %s\n", path)
	oldStart, oldCount, newStart, newCount := diffHunkRange(ops)
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
	for _, op := range ops {
		switch op.kind {
		case diffEqual:
			fmt.Fprintf(&b, " %s\n", op.text)
		case diffDelete:
			fmt.Fprintf(&b, "-%s\n", op.text)
		case diffInsert:
			fmt.Fprintf(&b, "+%s\n", op.text)
		}
	}
	return b.String()
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
	return trimDiffContext(ops, 3)
}

func trimDiffContext(ops []diffOp, contextLines int) []diffOp {
	first, last := -1, -1
	for i, op := range ops {
		if op.kind != diffEqual {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	if first == -1 {
		return nil
	}
	start := first - contextLines
	if start < 0 {
		start = 0
	}
	end := last + contextLines + 1
	if end > len(ops) {
		end = len(ops)
	}
	return ops[start:end]
}

func diffHunkRange(ops []diffOp) (int, int, int, int) {
	oldStart, newStart := 1, 1
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
	return oldStart, oldCount, newStart, newCount
}
