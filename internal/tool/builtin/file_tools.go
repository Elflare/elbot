// 实现文件读取与编辑工具，组织调用参数及结果。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"elbot/internal/contextinfo"
	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/workspace"
)

const (
	fileToolDetailedMaxBytes = int64(fileops.MaxFileSize)
	fileToolMaxBytes         = 100 * 1024 * 1024
	editDetailMaxBytes       = 4 * 1024
)

type ReadFileTool struct {
	FileGuard *FileGuard
	astCache  *directoryASTCache
}

type EditFileTool struct {
	FileGuard *FileGuard
	Rollback  *fileops.Service
}

type readFileArgs struct {
	Path         string             `json:"path"`
	Encoding     string             `json:"encoding"`
	StartLine    readFileInteger    `json:"start_line"`
	EndLine      fileops.LineNumber `json:"end_line"`
	Mode         string             `json:"mode"`
	Query        string             `json:"query"`
	ContextLines int                `json:"context_lines"`
	MaxMatches   int                `json:"max_matches"`
	Index        int                `json:"index"`
}

type readFileInteger int

func (n *readFileInteger) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "" || text == "null" {
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		value, err := strconv.Atoi(strings.TrimSpace(str))
		if err != nil {
			return fmt.Errorf("start_line must be an integer or integer string")
		}
		*n = readFileInteger(value)
		return nil
	}
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("start_line must be an integer or integer string")
	}
	*n = readFileInteger(value)
	return nil
}

type editFileArgs struct {
	Path             string         `json:"path"`
	Encoding         string         `json:"encoding"`
	ExpectedRevision string         `json:"expected_revision"`
	Create           bool           `json:"create"`
	ContextLines     int            `json:"context_lines"`
	Edits            []fileops.Edit `json:"edits"`
}

func NewReadFileTool(fileGuard ...*FileGuard) ReadFileTool {
	return ReadFileTool{FileGuard: firstFileGuard(fileGuard), astCache: newDirectoryASTCache()}
}

func (ReadFileTool) Name() string {
	return "read_file"
}

func (t ReadFileTool) Info() toolshare.Info {
	return readFileBuilder().BuildInfo()
}

func (t ReadFileTool) Schema() llm.ToolSchema {
	return readFileBuilder().BuildSchema()
}

func (t ReadFileTool) AssessRisk(ctx context.Context, req toolshare.CallRequest) (toolshare.RiskAssessment, error) {
	var args readFileArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return toolshare.RiskAssessment{}, fmt.Errorf("parse read_file arguments: %w", err)
		}
	}
	mode, err := normalizeReadFileMode(args.Mode)
	if err != nil {
		return toolshare.RiskAssessment{}, err
	}
	resolved, err := workspace.ResolveWorkspacePath(ctx, args.Path, workspace.PathResolveOptions{AllowDirectory: mode != readFileModeRead})
	if err != nil {
		return toolshare.RiskAssessment{}, err
	}
	if isSensitiveReadFile(resolved.Path) {
		return toolshare.RiskAssessment{Level: toolshare.RiskHigh, Reasons: []string{"读取可能包含凭据的敏感文件，需要用户确认"}}, nil
	}
	return toolshare.RiskAssessment{Level: toolshare.RiskLow, Reasons: []string{"读取普通文本文件"}}, nil
}

func isSensitiveReadFile(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	if name == ".env" || strings.HasPrefix(name, ".env.") {
		return true
	}
	switch name {
	case ".netrc", ".npmrc", ".pypirc", "credentials", "credentials.json", "kubeconfig", "id_rsa", "id_ed25519":
		return true
	}
	return strings.Contains(name, "secret") || strings.Contains(name, "token") || strings.Contains(name, "credential")
}

func readFileBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("read_file").
		Description("读取文本文件并返回行号和 revision；支持按行读取，以及在文件或目录中进行文本、AST 名称和函数名搜索。").
		Risk(toolshare.RiskLow).
		SuperadminOnly().
		Tags("files", "agent").
		DependsOn("workspace", "rollback_file").
		String("path", "文件或目录路径；read 模式仅支持文件，搜索模式可递归搜索目录。", toolshare.Required()).
		String("encoding", "文本编码，默认 auto。").
		String("mode", "模式：read（默认，可不填）、grep、ast、ast_function；ast/ast_function 仅支持 Go 和 Shell。", toolshare.Enum("read", "grep", "ast", "ast_function")).
		String("query", "grep/ast/ast_function 模式的搜索内容；ast 按名称精确匹配，ast_function 按函数名精确匹配。").
		Integer("index", "可选，搜索结果序号，默认不填。grep/ast/ast_function返回内容有多个时，根据返回内容的编号再次搜索时填写。").
		Integer("start_line", "read 模式起始行，1-based，默认 1。").
		String("end_line", "read 模式结束行（含），可传 end；默认最多读取 200 行。").
		Integer("context_lines", "grep/ast 模式上下文行数，默认 2，范围 0-20。").
		Integer("max_matches", "grep/ast/ast_function 模式最大匹配数，默认 20，范围 1-100。")
}

func (t ReadFileTool) Call(ctx context.Context, req toolshare.CallRequest) (_ *toolshare.Result, logContent string, _ error) {
	defer func() { logContent = toolshare.TruncateLogContent(logContent, 1024) }()
	var args readFileArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			{
				callErr := fmt.Errorf("parse read_file arguments: %w", err)
				return nil, callErr.Error(), callErr
			}
		}
	}
	mode, err := normalizeReadFileMode(args.Mode)
	if err != nil {
		return nil, err.Error(), err
	}
	resolved, err := workspace.ResolveWorkspacePath(ctx, args.Path, workspace.PathResolveOptions{AllowDirectory: mode != readFileModeRead})
	if err != nil {
		return nil, err.Error(), err
	}
	path := resolved.Path
	var warnings []string
	if resolved.WasAbs && !contextinfo.BackgroundContext(ctx) {
		warnings = append(warnings, toolshare.AbsolutePathWorkspaceWarning)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err.Error(), err
	}
	if info.IsDir() {
		if mode == readFileModeRead {
			{
				callErr := fmt.Errorf("read mode requires a file path")
				return nil, callErr.Error(), callErr
			}
		}
		{
			result, callErr := readFileDirectorySearch(ctx, path, mode, args, warnings, t.astCache)
			logContent := ""
			if result != nil {
				logContent = result.Content
			}
			if callErr != nil {
				logContent = callErr.Error() + "\n" + logContent
			}
			return result, logContent, callErr
		}
	}
	if info.Size() > fileToolMaxBytes {
		{
			callErr := fmt.Errorf("file too large: %d bytes exceeds %d", info.Size(), fileToolMaxBytes)
			return nil, callErr.Error(), callErr
		}
	}
	largeFile := info.Size() > fileToolDetailedMaxBytes
	if largeFile && (mode == readFileModeAST || mode == readFileModeASTFunction) {
		{
			callErr := fmt.Errorf("%s mode is disabled for this %d-byte file; use read or grep mode", mode, info.Size())
			return nil, callErr.Error(), callErr
		}
	}
	file, err := fileops.ReadFileWithLimit(path, args.Encoding, fileToolMaxBytes)
	if err != nil {
		return nil, err.Error(), err
	}
	lines := fileops.SplitLines(file.Text)
	warnings = append(warnings, t.FileGuard.ReadWarnings(path)...)
	if largeFile {
		warnings = append(warnings, fmt.Sprintf("large file: %d bytes; ast and ast_function modes are disabled", info.Size()))
	}
	switch mode {
	case readFileModeGrep:
		{
			result, callErr := readFileGrepResult(file, lines, args.Query, args.ContextLines, args.MaxMatches, args.Index, warnings)
			logContent := ""
			if result != nil {
				logContent = result.Content
			}
			if callErr != nil {
				logContent = callErr.Error() + "\n" + logContent
			}
			return result, logContent, callErr
		}
	case readFileModeAST:
		{
			result, callErr := readFileASTResult(file, args.Query, args.ContextLines, args.MaxMatches, args.Index, warnings)
			logContent := ""
			if result != nil {
				logContent = result.Content
			}
			if callErr != nil {
				logContent = callErr.Error() + "\n" + logContent
			}
			return result, logContent, callErr
		}
	case readFileModeASTFunction:
		{
			result, callErr := readFileASTFunctionResult(file, args.Query, args.MaxMatches, args.Index, warnings)
			logContent := ""
			if result != nil {
				logContent = result.Content
			}
			if callErr != nil {
				logContent = callErr.Error() + "\n" + logContent
			}
			return result, logContent, callErr
		}
	}
	start, end, truncated, err := fileops.NormalizeReadRange(len(lines), int(args.StartLine), args.EndLine)
	if err != nil {
		return nil, err.Error(), err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "file: %s\n", file.Path)
	fmt.Fprintf(&b, "encoding: %s\n", file.Encoding)
	fmt.Fprintf(&b, "revision: %s\n", fileops.ContentRevision(file.Bytes))
	if len(lines) == 0 {
		fmt.Fprintf(&b, "lines: 0/0\n")
		fmt.Fprintf(&b, "empty: true\n")
		fmt.Fprintf(&b, "truncated: %t\n", truncated)
		b.WriteString("content:\n")
		{
			result := &toolshare.Result{Content: b.String(), Warnings: warnings}
			return result, fmt.Sprintf("file=%s mode=%s lines=0/0 empty=true", file.Path, mode), nil
		}
	}

	fmt.Fprintf(&b, "lines: %d-%d/%d\n", start, end, len(lines))
	fmt.Fprintf(&b, "truncated: %t\n", truncated)
	b.WriteString("content:\n")
	width := len(fmt.Sprintf("%d", end))
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%*d | %s\n", width, i, lines[i-1])
	}
	{
		result := &toolshare.Result{Content: b.String(), Warnings: warnings}
		return result, fmt.Sprintf("file=%s mode=%s lines=%d-%d/%d truncated=%t\n%s", file.Path, mode, start, end, len(lines), truncated, strings.Join(lines[start-1:end], "\n")), nil
	}
}

func readFileGrepResult(file fileops.File, lines []string, grep string, contextLines, maxMatches, index int, warnings []string) (*toolshare.Result, error) {
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
	if index > 0 {
		if index > len(matches) {
			return nil, fmt.Errorf("index %d is out of range; found %d matches", index, len(matches))
		}
		matches = matches[index-1 : index]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "file: %s\n", file.Path)
	fmt.Fprintf(&b, "encoding: %s\n", file.Encoding)
	fmt.Fprintf(&b, "revision: %s\n", fileops.ContentRevision(file.Bytes))
	fmt.Fprintf(&b, "grep: %q\n", query)
	fmt.Fprintf(&b, "matches: %d\n", len(matches))
	if index > 0 {
		fmt.Fprintf(&b, "index: %d\n", index)
	}
	fmt.Fprintf(&b, "context_lines: %d\n", contextLines)
	b.WriteString("content:\n")
	if len(matches) == 0 || len(lines) == 0 {
		return &toolshare.Result{Content: b.String(), Warnings: warnings}, nil
	}
	width := len(fmt.Sprintf("%d", len(lines)))
	lastPrinted := 0
	for matchIndex, matchLine := range matches {
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
		fmt.Fprintf(&b, "%d. match: %d\n", matchIndex+1, matchLine)
		for i := start; i <= end; i++ {
			marker := " "
			if i == matchLine {
				marker = ">"
			}
			fmt.Fprintf(&b, "%s %*d | %s\n", marker, width, i, lines[i-1])
		}
		lastPrinted = end
	}
	return &toolshare.Result{Content: b.String(), Warnings: warnings}, nil
}

func NewEditFileTool(fileGuard ...*FileGuard) EditFileTool {
	guard := firstFileGuard(fileGuard)
	return EditFileTool{FileGuard: guard, Rollback: fileops.NewService(guard.CheckWrite)}
}

func (EditFileTool) Name() string {
	return "edit_file"
}

func (t EditFileTool) Info() toolshare.Info {
	return editFileBuilder().BuildInfo()
}

func (t EditFileTool) Schema() llm.ToolSchema {
	return editFileBuilder().BuildSchema()
}

func editFileBuilder() *toolshare.Builder {
	editProperties := fileops.EditOperationProperties()
	return toolshare.NewBuilder("edit_file").
		Description("批量编辑文本文件；使用 edits 一次提交多个修改；成功后返回 unified diff。任一 edit 失败则不写文件。").
		Risk(toolshare.RiskHigh).
		DependsOn("workspace", "rollback_file").
		Tags("files", "agent").
		String("path", "要编辑的文件路径,基于当前 workspace 解析；也可传绝对路径。", toolshare.Required()).
		String("encoding", "文本编码，默认 auto；非 UTF-8 文件应显式传入 gb18030、gbk、big5、shift_jis 等。").
		String("expected_revision", "可选，编辑前读取到的revision；用于防止外部并发修改。").
		Boolean("create", "为 true 时允许创建不存在的文本文件；提供 expected_revision 时仍要求文件已存在。").
		Integer("context_lines", "diff 上下文行数，默认 3，范围 0-20。确认前自动预检和实际写入结果都会使用该上下文行数。").
		ObjectArray("edits", "批量编辑列表；所有目标均基于编辑前原文解析。编辑已有文件时，应在本次调用的顶层传入 expected_revision，其值来自 read_file 返回的 revision; 创建新文件时不提供该字段。", editProperties, []string{"operation"}, toolshare.Required())
}

func (t EditFileTool) AssessRisk(ctx context.Context, req toolshare.CallRequest) (toolshare.RiskAssessment, error) {
	var args editFileArgs
	if len(req.Arguments) > 0 {
		if err := decodeStrictFileArgs(req.Arguments, &args); err != nil {
			return toolshare.RiskAssessment{}, fmt.Errorf("parse edit_file arguments: %w", err)
		}
	}
	resolved, err := resolveFileToolPath(ctx, args.Path, args.Create)
	if err != nil {
		return toolshare.RiskAssessment{}, err
	}
	if err := checkFileToolWrite(t.FileGuard, resolved.Path, args.Create); err != nil {
		return toolshare.RiskAssessment{}, err
	}
	if scope, ok := workspace.PathScopeFromContext(ctx); ok && scope.Restricted {
		return toolshare.RiskAssessment{Level: toolshare.RiskMedium, Reasons: []string{"文件编辑限制在当前调用工作目录内"}}, nil
	}
	return toolshare.RiskAssessment{Level: toolshare.RiskHigh, Reasons: []string{"文件内容写入操作需要确认"}}, nil
}

func (t EditFileTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	var args editFileArgs
	if len(req.Arguments) > 0 {
		if err := decodeStrictFileArgs(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse edit_file arguments: %w", err)
		}
	}
	prepared, err := t.Rollback.PrepareEdit(ctx, editServiceRequest(args))
	if err != nil {
		return nil, err
	}
	detail := editFileRiskDetail(args, prepared.Preview())
	return toolshare.NewPreparedCall(req, detail, func(ctx context.Context) (*toolshare.Result, string, error) {
		return t.call(ctx, args, prepared)
	}), nil
}

func editFileRiskDetail(args editFileArgs, preview fileops.EditResult) string {
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
	if strings.TrimSpace(args.ExpectedRevision) != "" {
		b.WriteString("文件 revision 校验：有\n")
	}
	for i, edit := range args.Edits {
		fmt.Fprintf(&b, "\n编辑 %d/%d：%s\n", i+1, len(args.Edits), editOperationTitle(edit.Operation))
		writeEditLocation(&b, edit)
		writeEditMatchDetail(&b, edit)
		writeEditContentDetail(&b, edit)
	}
	b.WriteString("\n预检 diff:\n")
	b.WriteString(preview.Diff)
	if !strings.HasSuffix(preview.Diff, "\n") {
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func fileToolBoolText(value bool) string {
	if value {
		return "是"
	}
	return "否"
}

func editOperationTitle(operation string) string {
	switch operation {
	case "replace_text":
		return "精确文本替换"
	case "replace":
		return "按行号范围替换"
	case "insert":
		return "按行间位置插入"
	case "delete":
		return "按行号删除"
	case "insert_before":
		return "在 anchor 行前插入"
	case "insert_after":
		return "在 anchor 行后插入"
	case "replace_line":
		return "替换 anchor 行"
	case "delete_line":
		return "删除 anchor 行"
	case "overwrite":
		return "覆盖整个文件"
	default:
		if strings.TrimSpace(operation) == "" {
			return "未知操作"
		}
		return operation
	}
}

func writeEditLocation(b *strings.Builder, edit fileops.Edit) {
	switch edit.Operation {
	case "insert":
		if edit.Line > 0 {
			fmt.Fprintf(b, "原始行间位置：%d\n", edit.Line)
		}
	case "replace", "delete":
		if edit.Line > 0 {
			fmt.Fprintf(b, "原始位置：%s\n", editLineRangeText(edit.Line, edit.EndLine))
		}
	}
}

func editLineRangeText(start, end int) string {
	if end <= 0 || end == start {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d-%d", start, end)
}

func writeEditMatchDetail(b *strings.Builder, edit fileops.Edit) {
	var matchText string
	switch edit.Operation {
	case "replace_text":
		matchText = edit.OldText
	case "insert_before", "insert_after", "replace_line", "delete_line":
		matchText = edit.Anchor
	default:
		return
	}
	if edit.Index != nil {
		fmt.Fprintf(b, "第几处匹配：%d\n", *edit.Index)
	}
	if edit.AllMatches {
		b.WriteString("匹配范围：全部\n")
	}
	writeIndentedBlock(b, "匹配内容：", matchText)
}

func writeEditContentDetail(b *strings.Builder, edit fileops.Edit) {
	if edit.NewText == nil {
		return
	}
	switch edit.Operation {
	case "insert", "insert_before", "insert_after":
		writeIndentedBlock(b, "插入内容：", *edit.NewText)
	case "overwrite":
		writeIndentedBlock(b, "文件新内容：", *edit.NewText)
	default:
		writeIndentedBlock(b, "新内容：", *edit.NewText)
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
	originalBytes := len(text)
	originalLines := strings.Count(text, "\n") + 1
	displayed, truncated := truncateUTF8Bytes(text, editDetailMaxBytes)
	for _, line := range strings.Split(displayed, "\n") {
		b.WriteString("  " + line + "\n")
	}
	if truncated {
		fmt.Fprintf(b, "  ...（已省略；原文 %d 字节，%d 行）\n", originalBytes, originalLines)
	}
}

func truncateUTF8Bytes(text string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text, false
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end], true
}

func fileToolEditOptions() fileops.EditFileOptions {
	return fileops.EditFileOptions{
		MaxInputBytes:        fileToolMaxBytes,
		MaxOutputBytes:       fileToolMaxBytes,
		DetailedDiffMaxBytes: fileToolDetailedMaxBytes,
	}
}
func (t EditFileTool) Call(ctx context.Context, req toolshare.CallRequest) (_ *toolshare.Result, logContent string, _ error) {
	var args editFileArgs
	if len(req.Arguments) > 0 {
		if err := decodeStrictFileArgs(req.Arguments, &args); err != nil {
			{
				callErr := fmt.Errorf("parse edit_file arguments: %w", err)
				return nil, toolshare.TruncateLogContent(callErr.Error(), 1024), callErr
			}
		}
	}
	return t.call(ctx, args, nil)
}

func (t EditFileTool) call(ctx context.Context, args editFileArgs, prepared *fileops.PreparedEdit) (_ *toolshare.Result, logContent string, _ error) {
	defer func() { logContent = toolshare.TruncateLogContent(logContent, 1024) }()
	var result fileops.EditResult
	var resolved workspace.ResolvedPath
	var err error
	if prepared != nil {
		result, resolved, err = prepared.Commit(ctx)
	} else {
		result, resolved, err = t.Rollback.Edit(ctx, editServiceRequest(args))
	}
	if err != nil {
		return nil, err.Error(), err
	}
	content := fmt.Sprintf("dry_run: %t\nedited: %s\ncreated: %t\nencoding: %s\nrevision_before: %s\nrevision_after: %s\ndiff:\n%s", result.DryRun, result.Path, result.Created, result.Encoding, result.RevisionBefore, result.RevisionAfter, result.Diff)
	content += fmt.Sprintf("\nrollback_available: %t", result.RollbackAvailable)
	var warnings []string
	if resolved.WasAbs && !contextinfo.BackgroundContext(ctx) {
		warnings = append(warnings, toolshare.AbsolutePathWorkspaceWarning)
	}
	if result.RollbackEvicted > 0 {
		warnings = append(warnings, fmt.Sprintf("撤销备份达到内存上限，已清理 %d 条最旧记录。", result.RollbackEvicted))
	}
	if lease, _ := t.Rollback.EditSession(ctx); lease != nil && !result.RollbackAvailable {
		warnings = append(warnings, "文件已编辑，但撤销备份因会话失效或容量限制未保留。")
	}
	{
		logContent = fmt.Sprintf("edited=%s created=%t dry_run=%t rollback_available=%t\ndiff:\n%s", result.Path, result.Created, result.DryRun, result.RollbackAvailable, result.Diff)
		return &toolshare.Result{Content: content, Warnings: warnings}, logContent, nil
	}
}

func editServiceRequest(args editFileArgs) fileops.EditRequest {
	return fileops.EditRequest{Path: args.Path, Encoding: args.Encoding, ExpectedRevision: args.ExpectedRevision, Create: args.Create, ContextLines: args.ContextLines, Edits: args.Edits, Options: fileToolEditOptions()}
}

func decodeStrictFileArgs(raw json.RawMessage, args any) error {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(args)
}

func checkFileToolWrite(guard *FileGuard, path string, create bool) error {
	if err := guard.CheckWrite(path); err != nil {
		return err
	}
	target, err := fileops.ResolveFileTarget(path, create)
	if err != nil {
		return err
	}
	return guard.CheckWrite(target)
}

func resolveFileToolPath(ctx context.Context, rawPath string, allowCreate bool) (workspace.ResolvedPath, error) {
	return workspace.ResolveWorkspacePath(ctx, rawPath, workspace.PathResolveOptions{AllowCreate: allowCreate})
}
