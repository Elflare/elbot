package builtin

import (
	"bytes"
	"context"
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

	"elbot/internal/llm"
	"elbot/internal/tool"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
)

const (
	defaultReadLineLimit = 200
	maxFileToolSize      = 2 * 1024 * 1024
	maxDiffCells         = 2_000_000
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

type readFileArgs struct {
	Path         string     `json:"path"`
	Encoding     string     `json:"encoding"`
	StartLine    int        `json:"start_line"`
	EndLine      lineNumber `json:"end_line"`
	Grep         string     `json:"grep"`
	ContextLines int        `json:"context_lines"`
	MaxMatches   int        `json:"max_matches"`
}

type editFileArgs struct {
	Path           string          `json:"path"`
	Encoding       string          `json:"encoding"`
	ExpectedSHA256 string          `json:"expected_sha256"`
	Create         bool            `json:"create"`
	DryRun         bool            `json:"dry_run"`
	ContextLines   int             `json:"context_lines"`
	Edits          []editOperation `json:"edits"`
}

type editOperation struct {
	Operation       string     `json:"operation"`
	StartLine       int        `json:"start_line"`
	EndLine         lineNumber `json:"end_line"`
	Content         string     `json:"content"`
	ExpectedContent *string    `json:"expected_content"`
	OldContent      string     `json:"old_content"`
	Anchor          string     `json:"anchor"`
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
		Tags("files", "agent").
		String("path", "要读取的文件路径", tool.Required()).
		String("encoding", "文本编码，默认 auto；可选 utf-8、utf-8-bom、utf-16le、utf-16be、gbk、gb18030、big5、shift_jis 等。").
		Integer("start_line", "起始行号，1-based；默认 1。").
		String("end_line", "结束行号，1-based 且包含该行；也可传 end 表示文件末尾；默认最多返回 200 行。").
		String("grep", "可选，按子串搜索匹配行；提供后返回匹配行及上下文，而不是普通行范围读取。").
		Integer("context_lines", "grep 上下文行数，默认 2，范围 0-20。").
		Integer("max_matches", "grep 最大匹配数，默认 20，范围 1-100。")
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
	if strings.TrimSpace(args.Grep) != "" {
		return readFileGrepResult(file, lines, args.Grep, args.ContextLines, args.MaxMatches)
	}
	start, end, truncated, err := normalizeReadRange(len(lines), args.StartLine, args.EndLine)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "file: %s\n", file.Path)
	fmt.Fprintf(&b, "encoding: %s\n", file.Encoding)
	fmt.Fprintf(&b, "sha256: %s\n", sha256Hex(file.Bytes))
	if len(lines) == 0 {
		fmt.Fprintf(&b, "lines: 0/0\n")
		fmt.Fprintf(&b, "empty: true\n")
		fmt.Fprintf(&b, "truncated: %t\n", truncated)
		b.WriteString("content:\n")
		return &tool.Result{Content: b.String()}, nil
	}

	fmt.Fprintf(&b, "lines: %d-%d/%d\n", start, end, len(lines))
	fmt.Fprintf(&b, "truncated: %t\n", truncated)
	b.WriteString("content:\n")
	width := len(fmt.Sprintf("%d", end))
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%*d | %s\n", width, i, lines[i-1])
	}
	return &tool.Result{Content: b.String()}, nil
}

func readFileGrepResult(file decodedFile, lines []string, grep string, contextLines, maxMatches int) (*tool.Result, error) {
	query := strings.TrimSpace(grep)
	if query == "" {
		return nil, fmt.Errorf("grep is required")
	}
	contextLines = normalizeGrepContextLines(contextLines)
	maxMatches = normalizeMaxMatches(maxMatches)
	matches := make([]int, 0)
	for i, line := range lines {
		if strings.Contains(line, query) {
			matches = append(matches, i+1)
			if len(matches) >= maxMatches {
				break
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "file: %s\n", file.Path)
	fmt.Fprintf(&b, "encoding: %s\n", file.Encoding)
	fmt.Fprintf(&b, "sha256: %s\n", sha256Hex(file.Bytes))
	fmt.Fprintf(&b, "grep: %q\n", query)
	fmt.Fprintf(&b, "matches: %d\n", len(matches))
	fmt.Fprintf(&b, "context_lines: %d\n", contextLines)
	b.WriteString("content:\n")
	if len(matches) == 0 || len(lines) == 0 {
		return &tool.Result{Content: b.String()}, nil
	}
	width := len(fmt.Sprintf("%d", len(lines)))
	lastPrinted := 0
	for _, matchLine := range matches {
		start := matchLine - contextLines
		if start < 1 {
			start = 1
		}
		end := matchLine + contextLines
		if end > len(lines) {
			end = len(lines)
		}
		if lastPrinted > 0 && start > lastPrinted+1 {
			b.WriteString("--\n")
		}
		if start <= lastPrinted {
			start = lastPrinted + 1
		}
		for i := start; i <= end; i++ {
			marker := " "
			if i == matchLine {
				marker = ">"
			}
			fmt.Fprintf(&b, "%s %*d | %s\n", marker, width, i, lines[i-1])
		}
		lastPrinted = end
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
	editProperties := map[string]any{
		"operation":        map[string]any{"type": "string", "description": "编辑操作：replace、delete、insert_line_before、insert_line_after、prepend、append、replace_match、delete_match、insert_before_match、insert_after_match。edits 按顺序应用，后一步基于前一步结果；工具不会重排序或补偿行号。"},
		"start_line":       map[string]any{"type": "integer", "description": "行号操作的起始行号，1-based。"},
		"end_line":         map[string]any{"type": "string", "description": "行号 replace/delete 的结束行号，1-based 且包含该行；也可传 end；默认等于 start_line。"},
		"content":          map[string]any{"type": "string", "description": "replace/insert 写入的文本；delete/delete_match 会忽略该字段。insert_line_before/insert_line_after/prepend/append 会按整行插入，自动补末尾换行。"},
		"expected_content": map[string]any{"type": "string", "description": "replace/delete 前校验目标行范围原始文本；换行符按 \\n 规范化比较，用于防止行号漂移误改；不需要校验时请省略该字段，不要传空字符串。"},
		"old_content":      map[string]any{"type": "string", "description": "replace_match/delete_match 要唯一匹配的原始文本；找不到或多处匹配都会失败。"},
		"anchor":           map[string]any{"type": "string", "description": "insert_before_match/insert_after_match 要唯一匹配的锚点文本；找不到或多处匹配都会失败。"},
	}
	return tool.NewBuilder("edit_file").
		Description("批量编辑文本文件；使用 edits 一次提交多个修改，支持多种方式；成功后返回 unified diff。任一 edit 失败则不写文件。").
		Risk(tool.RiskHigh).
		Tags("files", "agent").
		String("path", "要编辑的文件路径。", tool.Required()).
		String("encoding", "文本编码，默认 auto；非 UTF-8 文件应显式传入 gb18030、gbk、big5、shift_jis 等。").
		String("expected_sha256", "可选，编辑前文件 sha256；用于防止外部并发修改。").
		Boolean("create", "为 true 时允许创建不存在的文本文件；提供 expected_sha256 时仍要求文件已存在。").
		Boolean("dry_run", "为 true 时只执行校验并返回 diff，不写入文件。").
		Integer("context_lines", "diff 上下文行数，默认 3，范围 0-20。").
		ObjectArray("edits", "批量编辑列表，按顺序应用；连续编辑同一文件时优先使用 match/anchor 操作，行号 replace/delete 建议提供 expected_content。", editProperties, []string{"operation"}, tool.Required())
}

func (EditFileTool) AssessRisk(ctx context.Context, req tool.CallRequest) (tool.RiskAssessment, error) {
	var args editFileArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return tool.RiskAssessment{}, fmt.Errorf("parse edit_file arguments: %w", err)
		}
	}
	if _, err := resolveFileToolPath(ctx, args.Path, args.Create); err != nil {
		return tool.RiskAssessment{}, err
	}
	if sandbox, ok := tool.SandboxContextFromContext(ctx); ok && sandbox.BackgroundKind == tool.BackgroundKindCron {
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
	path, err := resolveFileToolPath(ctx, args.Path, args.Create)
	if err != nil {
		return nil, err
	}
	file, created, err := readOrCreateDecodedFile(path, args.Encoding, args.Create, strings.TrimSpace(args.ExpectedSHA256) != "")
	if err != nil {
		return nil, err
	}
	if expected := strings.TrimSpace(args.ExpectedSHA256); expected != "" && !strings.EqualFold(expected, sha256Hex(file.Bytes)) {
		return nil, fmt.Errorf("file sha256 mismatch: current %s", sha256Hex(file.Bytes))
	}
	oldText := normalizeEditText(file.Text)
	newText, err := applyEdits(oldText, args.Edits)
	if err != nil {
		return nil, err
	}
	if newText == oldText {
		return nil, fmt.Errorf("edit produced no changes")
	}
	outputText := restoreLineEndings(newText, file.LineEnding)
	newBytes, err := encodeText(outputText, file.Encoding, file.BOM)
	if err != nil {
		return nil, err
	}
	contextLines := normalizeContextLines(args.ContextLines)
	diff := unifiedDiff(file.Path, splitLines(oldText), splitLines(newText), contextLines)
	content := fmt.Sprintf("dry_run: %t\nedited: %s\ncreated: %t\nencoding: %s\nsha256_before: %s\nsha256_after: %s\ndiff:\n%s", args.DryRun, file.Path, created, file.Encoding, sha256Hex(file.Bytes), sha256Hex(newBytes), diff)
	if args.DryRun {
		return &tool.Result{Content: content}, nil
	}
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	} else if !created {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	if err := atomicWriteFile(path, newBytes, mode); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}
	return &tool.Result{Content: content}, nil
}

func resolveFileToolPath(ctx context.Context, rawPath string, allowCreate bool) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", fmt.Errorf("path is required")
	}
	expandedPath, err := expandHomePath(rawPath)
	if err != nil {
		return "", err
	}
	if sandbox, ok := tool.SandboxContextFromContext(ctx); ok && sandbox.Background {
		root := strings.TrimSpace(sandbox.Dir)
		if root == "" {
			root = strings.TrimSpace(sandbox.Root)
		}
		if root == "" {
			return "", fmt.Errorf("background sandbox is not configured")
		}
		return resolveInsideRoot(expandedPath, root)
	}
	path := filepath.Clean(expandedPath)
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve path: %w", err)
		}
		path = abs
	}
	if info, err := os.Stat(path); err != nil {
		if !allowCreate || !os.IsNotExist(err) {
			return "", fmt.Errorf("stat file: %w", err)
		}
	} else if info.IsDir() {
		return "", fmt.Errorf("path is a directory")
	}
	return path, nil
}

func expandHomePath(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("home directory is not configured")
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
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

func readOrCreateDecodedFile(path, requestedEncoding string, create bool, hasExpectedSHA bool) (decodedFile, bool, error) {
	file, err := readDecodedFile(path, requestedEncoding)
	if err == nil {
		return file, false, nil
	}
	if !create || hasExpectedSHA || !errors.Is(err, os.ErrNotExist) {
		return decodedFile{}, false, err
	}
	encodingName := strings.ToLower(strings.TrimSpace(requestedEncoding))
	if encodingName == "" || encodingName == "auto" || encodingName == "utf8" {
		encodingName = "utf-8"
	}
	if encodingName == "utf-8-bom" {
		return decodedFile{Path: path, Encoding: "utf-8-bom", BOM: []byte{0xEF, 0xBB, 0xBF}, LineEnding: "\n"}, true, nil
	}
	if encodingName != "utf-8" {
		if _, err := lookupEncoding(encodingName); err != nil {
			return decodedFile{}, false, err
		}
	}
	return decodedFile{Path: path, Encoding: encodingName, LineEnding: "\n"}, true, nil
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

func applyEdits(text string, edits []editOperation) (string, error) {
	if len(edits) == 0 {
		return "", fmt.Errorf("edits is required")
	}
	current := normalizeEditText(text)
	for i, edit := range edits {
		next, err := applyEdit(current, edit)
		if err != nil {
			return "", fmt.Errorf("edit %d: %w", i+1, err)
		}
		current = next
	}
	return current, nil
}

func applyEdit(text string, edit editOperation) (string, error) {
	operation := strings.ToLower(strings.TrimSpace(edit.Operation))
	if operation == "" {
		return "", fmt.Errorf("operation is required")
	}
	switch operation {
	case "replace", "delete", "insert_line_before", "insert_line_after", "prepend", "append":
		return applyLineEdit(text, edit)
	case "replace_match":
		start, end, err := findUniqueMatch(text, edit.OldContent, "old_content")
		if err != nil {
			return "", err
		}
		return text[:start] + normalizeEditText(edit.Content) + text[end:], nil
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
		return text[:index] + normalizeEditText(edit.Content) + text[index:], nil
	default:
		return "", fmt.Errorf("unsupported operation %q", edit.Operation)
	}
}

func applyLineEdit(text string, edit editOperation) (string, error) {
	lines := splitLines(text)
	endsNewline := strings.HasSuffix(text, "\n")
	operation := strings.ToLower(strings.TrimSpace(edit.Operation))
	start := edit.StartLine
	end := edit.EndLine.Value
	if edit.EndLine.End {
		end = len(lines)
	} else if end == 0 {
		end = start
	}
	contentLines := splitLines(edit.Content)
	lineContentLines := splitLines(ensureTrailingNewline(edit.Content))
	out := append([]string{}, lines...)
	switch operation {
	case "replace":
		if err := validateLineRange(len(lines), start, end); err != nil {
			return "", err
		}
		if err := validateExpectedContent(lines, start, end, edit.ExpectedContent); err != nil {
			return "", err
		}
		out = replaceLines(out, start, end, contentLines)
	case "delete":
		if err := validateLineRange(len(lines), start, end); err != nil {
			return "", err
		}
		if err := validateExpectedContent(lines, start, end, edit.ExpectedContent); err != nil {
			return "", err
		}
		out = replaceLines(out, start, end, nil)
	case "insert_line_before", "insert_line_after":
		if err := validateInsertLine(len(lines), start); err != nil {
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
	return joinLines(out, "\n", endsNewline), nil
}

func validateExpectedContent(lines []string, start, end int, expected *string) error {
	if expected == nil || *expected == "" {
		return nil
	}
	actual := strings.Join(lines[start-1:end], "\n")
	want := strings.TrimSuffix(normalizeEditText(*expected), "\n")
	if actual != want {
		return fmt.Errorf("target content mismatch at lines %d-%d", start, end)
	}
	return nil
}

func findUniqueMatch(text, rawNeedle, label string) (int, int, error) {
	needle := normalizeEditText(rawNeedle)
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

func ensureTrailingNewline(text string) string {
	text = normalizeEditText(text)
	if text == "" || strings.HasSuffix(text, "\n") {
		return text
	}
	return text + "\n"
}

func normalizeEditText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

func restoreLineEndings(text, lineEnding string) string {
	if lineEnding == "" || lineEnding == "\n" {
		return text
	}
	return strings.ReplaceAll(text, "\n", lineEnding)
}

func normalizeGrepContextLines(value int) int {
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

func normalizeMaxMatches(value int) int {
	if value <= 0 {
		return 20
	}
	if value > 100 {
		return 100
	}
	return value
}

func normalizeContextLines(value int) int {
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

type diffHunk struct {
	ops      []diffOp
	oldStart int
	newStart int
}

func unifiedDiff(path string, oldLines, newLines []string, contextLines int) string {
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

func buildDiffHunks(ops []diffOp, contextLines int) []diffHunk {
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
	hunks := make([]diffHunk, 0, len(ranges))
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

func trimDiffContextRange(ops []diffOp, first, last, contextLines int) diffHunk {
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
	return diffHunk{ops: ops[start:end], oldStart: oldStart, newStart: newStart}
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
