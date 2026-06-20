package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/tool"
	"elbot/internal/utils/fileops"
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

func (n lineNumber) fileops() fileops.LineNumber {
	return fileops.LineNumber{Value: n.Value, End: n.End}
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

func (e editOperation) fileops() fileops.Edit {
	return fileops.Edit{
		Operation:       e.Operation,
		StartLine:       e.StartLine,
		EndLine:         e.EndLine.fileops(),
		Content:         e.Content,
		ExpectedContent: e.ExpectedContent,
		OldContent:      e.OldContent,
		Anchor:          e.Anchor,
	}
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
	file, err := fileops.ReadFile(path, args.Encoding)
	if err != nil {
		return nil, err
	}
	lines := fileops.SplitLines(file.Text)
	if strings.TrimSpace(args.Grep) != "" {
		return readFileGrepResult(file, lines, args.Grep, args.ContextLines, args.MaxMatches)
	}
	start, end, truncated, err := fileops.NormalizeReadRange(len(lines), args.StartLine, args.EndLine.fileops())
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "file: %s\n", file.Path)
	fmt.Fprintf(&b, "encoding: %s\n", file.Encoding)
	fmt.Fprintf(&b, "sha256: %s\n", fileops.SHA256Hex(file.Bytes))
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

func readFileGrepResult(file fileops.File, lines []string, grep string, contextLines, maxMatches int) (*tool.Result, error) {
	query := strings.TrimSpace(grep)
	if query == "" {
		return nil, fmt.Errorf("grep is required")
	}
	contextLines = fileops.NormalizeGrepContextLines(contextLines)
	maxMatches = fileops.NormalizeMaxMatches(maxMatches)
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
	fmt.Fprintf(&b, "sha256: %s\n", fileops.SHA256Hex(file.Bytes))
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
		if err := decodeEditArgs(req.Arguments, &args); err != nil {
			return tool.RiskAssessment{}, fmt.Errorf("parse edit_file arguments: %w", err)
		}
	}
	if _, err := resolveFileToolPath(ctx, args.Path, args.Create); err != nil {
		return tool.RiskAssessment{}, err
	}
	if sandbox, ok := tool.SandboxContextFromContext(ctx); ok && sandbox.Background {
		return tool.RiskAssessment{Level: tool.RiskMedium, Reasons: []string{"后台文件编辑限制在当前任务工作目录内"}}, nil
	}
	return tool.RiskAssessment{Level: tool.RiskHigh, Reasons: []string{"文件内容写入操作需要确认"}}, nil
}

func (EditFileTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args editFileArgs
	if len(req.Arguments) > 0 {
		if err := decodeEditArgs(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse edit_file arguments: %w", err)
		}
	}
	path, err := resolveFileToolPath(ctx, args.Path, args.Create)
	if err != nil {
		return nil, err
	}
	edits := make([]fileops.Edit, 0, len(args.Edits))
	for _, edit := range args.Edits {
		edits = append(edits, edit.fileops())
	}
	result, err := fileops.EditFile(path, args.Encoding, args.ExpectedSHA256, args.Create, args.DryRun, args.ContextLines, edits)
	if err != nil {
		return nil, err
	}
	content := fmt.Sprintf("dry_run: %t\nedited: %s\ncreated: %t\nencoding: %s\nsha256_before: %s\nsha256_after: %s\ndiff:\n%s", result.DryRun, result.Path, result.Created, result.Encoding, result.SHA256Before, result.SHA256After, result.Diff)
	return &tool.Result{Content: content}, nil
}

func decodeEditArgs(raw json.RawMessage, args *editFileArgs) error {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(args)
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
		path, err := tool.ResolveSandboxRelativePath(sandbox, rawPath)
		if err != nil {
			return "", err
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
