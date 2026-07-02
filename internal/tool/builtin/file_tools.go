package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/tool"
	"elbot/internal/utils/fileops"
)

type ReadFileTool struct {
	FileGuard *FileGuard
}

type EditFileTool struct {
	FileGuard *FileGuard
}

type readFileArgs struct {
	Path         string             `json:"path"`
	Encoding     string             `json:"encoding"`
	StartLine    int                `json:"start_line"`
	EndLine      fileops.LineNumber `json:"end_line"`
	Grep         string             `json:"grep"`
	ContextLines int                `json:"context_lines"`
	MaxMatches   int                `json:"max_matches"`
}

type editFileArgs struct {
	Path           string         `json:"path"`
	Encoding       string         `json:"encoding"`
	ExpectedSHA256 string         `json:"expected_sha256"`
	Create         bool           `json:"create"`
	ContextLines   int            `json:"context_lines"`
	Edits          []fileops.Edit `json:"edits"`
}

func NewReadFileTool(fileGuard ...*FileGuard) ReadFileTool {
	return ReadFileTool{FileGuard: firstFileGuard(fileGuard)}
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
		DependsOn("workspace").
		String("path", "要读取的文件路径", tool.Required()).
		String("encoding", "文本编码，默认 auto；可选 utf-8、utf-8-bom、utf-16le、utf-16be、gbk、gb18030、big5、shift_jis 等。").
		Integer("start_line", "起始行号，1-based；默认 1。").
		String("end_line", "结束行号，1-based 且包含该行；也可传 end 表示文件末尾；默认最多返回 200 行。").
		String("grep", "可选，按子串搜索匹配行；提供后返回匹配行及上下文，而不是普通行范围读取。").
		Integer("context_lines", "grep 上下文行数，默认 2，范围 0-20。").
		Integer("max_matches", "grep 最大匹配数，默认 20，范围 1-100。")
}

func (t ReadFileTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args readFileArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse read_file arguments: %w", err)
		}
	}
	resolved, err := resolveFileToolPath(ctx, args.Path, false)
	if err != nil {
		return nil, err
	}
	path := resolved.Path
	file, err := fileops.ReadFile(path, args.Encoding)
	if err != nil {
		return nil, err
	}
	lines := fileops.SplitLines(file.Text)
	warnings := append(resolved.Warnings, t.FileGuard.ReadWarnings(path)...)
	if strings.TrimSpace(args.Grep) != "" {
		return readFileGrepResult(file, lines, args.Grep, args.ContextLines, args.MaxMatches, warnings)
	}
	start, end, truncated, err := fileops.NormalizeReadRange(len(lines), args.StartLine, args.EndLine)
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
		return &tool.Result{Content: b.String(), Warnings: warnings}, nil
	}

	fmt.Fprintf(&b, "lines: %d-%d/%d\n", start, end, len(lines))
	fmt.Fprintf(&b, "truncated: %t\n", truncated)
	b.WriteString("content:\n")
	width := len(fmt.Sprintf("%d", end))
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%*d | %s\n", width, i, lines[i-1])
	}
	return &tool.Result{Content: b.String(), Warnings: warnings}, nil
}

func readFileGrepResult(file fileops.File, lines []string, grep string, contextLines, maxMatches int, warnings []string) (*tool.Result, error) {
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
		return &tool.Result{Content: b.String(), Warnings: warnings}, nil
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
	return &tool.Result{Content: b.String(), Warnings: warnings}, nil
}

func NewEditFileTool(fileGuard ...*FileGuard) EditFileTool {
	return EditFileTool{FileGuard: firstFileGuard(fileGuard)}
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
	editProperties := fileops.EditOperationProperties()
	return tool.NewBuilder("edit_file").
		Description("批量编辑文本文件；使用 edits 一次提交多个修改，系统会在确认前自动预检并生成 diff，确认后才写入；成功后返回 unified diff。任一 edit 失败则不写文件。").
		Risk(tool.RiskHigh).
		DependsOn("workspace").
		Tags("files", "agent").
		String("path", "要编辑的文件路径。", tool.Required()).
		String("encoding", "文本编码，默认 auto；非 UTF-8 文件应显式传入 gb18030、gbk、big5、shift_jis 等。").
		String("expected_sha256", "可选，编辑前文件 sha256；用于防止外部并发修改。").
		Boolean("create", "为 true 时允许创建不存在的文本文件；提供 expected_sha256 时仍要求文件已存在。").
		Integer("context_lines", "diff 上下文行数，默认 3，范围 0-20。确认前自动预检和实际写入结果都会使用该上下文行数。").
		ObjectArray("edits", "批量编辑列表，按顺序应用；连续编辑同一文件时优先使用 match/anchor 操作，行号 replace/delete 建议提供 expected_content。", editProperties, []string{"operation"}, tool.Required())
}

func (t EditFileTool) AssessRisk(ctx context.Context, req tool.CallRequest) (tool.RiskAssessment, error) {
	var args editFileArgs
	if len(req.Arguments) > 0 {
		if err := decodeEditArgs(req.Arguments, &args); err != nil {
			return tool.RiskAssessment{}, fmt.Errorf("parse edit_file arguments: %w", err)
		}
	}
	resolved, err := resolveFileToolPath(ctx, args.Path, args.Create)
	if err != nil {
		return tool.RiskAssessment{}, err
	}
	if err := t.FileGuard.CheckWrite(resolved.Path); err != nil {
		return tool.RiskAssessment{}, err
	}
	if sandbox, ok := tool.SandboxContextFromContext(ctx); ok && sandbox.Background {
		return tool.RiskAssessment{Level: tool.RiskMedium, Reasons: []string{"后台文件编辑限制在当前任务工作目录内"}}, nil
	}
	return tool.RiskAssessment{Level: tool.RiskHigh, Reasons: []string{"文件内容写入操作需要确认"}}, nil
}

func (t EditFileTool) PreflightConfirmation(ctx context.Context, req tool.CallRequest) error {
	var args editFileArgs
	if len(req.Arguments) > 0 {
		if err := decodeEditArgs(req.Arguments, &args); err != nil {
			return fmt.Errorf("parse edit_file arguments: %w", err)
		}
	}
	_, err := previewEditFile(ctx, args, t.FileGuard)
	return err
}

func (t EditFileTool) RiskDetail(ctx context.Context, req tool.CallRequest) (string, error) {
	var args editFileArgs
	if len(req.Arguments) > 0 {
		if err := decodeEditArgs(req.Arguments, &args); err != nil {
			return "", fmt.Errorf("parse edit_file arguments: %w", err)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "文件：%s\n", strings.TrimSpace(args.Path))
	b.WriteString("模式：确认后写入；确认前已自动预检\n")
	fmt.Fprintf(&b, "创建文件：%s\n", fileToolBoolText(args.Create))
	if strings.TrimSpace(args.Encoding) != "" {
		fmt.Fprintf(&b, "编码：%s\n", strings.TrimSpace(args.Encoding))
	} else {
		b.WriteString("编码：auto\n")
	}
	fmt.Fprintf(&b, "编辑数：%d\n", len(args.Edits))
	if strings.TrimSpace(args.ExpectedSHA256) != "" {
		b.WriteString("文件哈希校验：有\n")
	}
	for i, edit := range args.Edits {
		fmt.Fprintf(&b, "\n编辑 %d/%d：%s\n", i+1, len(args.Edits), editOperationTitle(edit.Operation))
		writeEditLocation(&b, edit)
		if edit.ExpectedContent != nil {
			b.WriteString("旧内容校验：有\n")
			writeIndentedBlock(&b, "校验内容：", *edit.ExpectedContent)
		}
		writeEditMatchDetail(&b, edit)
		writeEditContentDetail(&b, edit)
	}
	preview, err := previewEditFile(ctx, args, t.FileGuard)
	if err != nil {
		return "", err
	}
	b.WriteString("\n预检 diff:\n")
	b.WriteString(preview.Diff)
	if !strings.HasSuffix(preview.Diff, "\n") {
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func fileToolBoolText(value bool) string {
	if value {
		return "是"
	}
	return "否"
}

func editOperationTitle(operation string) string {
	switch operation {
	case "replace":
		return "替换行"
	case "delete":
		return "删除行"
	case "insert_line_before":
		return "在指定行前插入"
	case "insert_line_after":
		return "在指定行后插入"
	case "prepend":
		return "插入到文件开头"
	case "append":
		return "追加到文件末尾"
	case "replace_match":
		return "按匹配替换"
	case "delete_match":
		return "按匹配删除"
	case "insert_before_match":
		return "按匹配插入到前面"
	case "insert_after_match":
		return "按匹配插入到后面"
	default:
		if strings.TrimSpace(operation) == "" {
			return "未知操作"
		}
		return operation
	}
}

func writeEditLocation(b *strings.Builder, edit fileops.Edit) {
	switch edit.Operation {
	case "replace", "delete":
		if edit.StartLine > 0 {
			fmt.Fprintf(b, "位置：%s\n", editLineRangeText(edit.StartLine, edit.EndLine))
		}
	case "insert_line_before", "insert_line_after":
		if edit.StartLine > 0 {
			fmt.Fprintf(b, "位置：第 %d 行\n", edit.StartLine)
		}
	}
}

func editLineRangeText(start int, end fileops.LineNumber) string {
	if end.End {
		return fmt.Sprintf("%d-end", start)
	}
	if end.Value <= 0 || end.Value == start {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d-%d", start, end.Value)
}

func writeEditMatchDetail(b *strings.Builder, edit fileops.Edit) {
	if !strings.Contains(edit.Operation, "_match") {
		return
	}
	mode := strings.TrimSpace(edit.MatchMode)
	if mode == "" {
		mode = "content"
	}
	fmt.Fprintf(b, "匹配方式：%s\n", mode)
	if edit.Index != nil {
		fmt.Fprintf(b, "第几处匹配：%d\n", *edit.Index)
	}
	matchText := edit.OldContent
	if edit.Operation == "insert_before_match" || edit.Operation == "insert_after_match" {
		matchText = edit.Anchor
	}
	writeIndentedBlock(b, "匹配内容：", matchText)
}

func writeEditContentDetail(b *strings.Builder, edit fileops.Edit) {
	switch edit.Operation {
	case "delete", "delete_match":
		return
	case "replace", "replace_match":
		writeIndentedBlock(b, "新内容：", edit.Content)
	case "insert_line_before", "insert_line_after", "insert_before_match", "insert_after_match":
		writeIndentedBlock(b, "插入内容：", edit.Content)
	case "prepend":
		writeIndentedBlock(b, "开头新增内容：", edit.Content)
	case "append":
		writeIndentedBlock(b, "末尾追加内容：", edit.Content)
	default:
		if edit.Content != "" {
			writeIndentedBlock(b, "内容：", edit.Content)
		}
	}
}

func writeIndentedBlock(b *strings.Builder, title, text string) {
	b.WriteString(title + "\n")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text == "" {
		b.WriteString("  (空)\n")
		return
	}
	for _, line := range strings.Split(text, "\n") {
		b.WriteString("  " + line + "\n")
	}
}

func (t EditFileTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args editFileArgs
	if len(req.Arguments) > 0 {
		if err := decodeEditArgs(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse edit_file arguments: %w", err)
		}
	}
	resolved, err := resolveFileToolPath(ctx, args.Path, args.Create)
	if err != nil {
		return nil, err
	}
	if err := t.FileGuard.CheckWrite(resolved.Path); err != nil {
		return nil, err
	}
	result, err := fileops.EditFile(resolved.Path, args.Encoding, args.ExpectedSHA256, args.Create, false, args.ContextLines, args.Edits)
	if err != nil {
		return nil, err
	}
	content := fmt.Sprintf("dry_run: %t\nedited: %s\ncreated: %t\nencoding: %s\nsha256_before: %s\nsha256_after: %s\ndiff:\n%s", result.DryRun, result.Path, result.Created, result.Encoding, result.SHA256Before, result.SHA256After, result.Diff)
	return &tool.Result{Content: content, Warnings: resolved.Warnings}, nil
}

func previewEditFile(ctx context.Context, args editFileArgs, fileGuard *FileGuard) (fileops.EditResult, error) {
	resolved, err := resolveFileToolPath(ctx, args.Path, args.Create)
	if err != nil {
		return fileops.EditResult{}, err
	}
	if err := fileGuard.CheckWrite(resolved.Path); err != nil {
		return fileops.EditResult{}, err
	}
	result, err := fileops.EditFile(resolved.Path, args.Encoding, args.ExpectedSHA256, args.Create, true, args.ContextLines, args.Edits)
	if err != nil {
		return fileops.EditResult{}, fmt.Errorf("preflight edit_file: %w", err)
	}
	return result, nil
}

func decodeEditArgs(raw json.RawMessage, args *editFileArgs) error {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(args)
}

func resolveFileToolPath(ctx context.Context, rawPath string, allowCreate bool) (tool.ResolvedPath, error) {
	return tool.ResolveWorkspacePath(ctx, rawPath, tool.PathResolveOptions{AllowCreate: allowCreate})
}
