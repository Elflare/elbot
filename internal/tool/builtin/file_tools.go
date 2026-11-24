// 实现文件读取与编辑工具，组织调用参数及结果。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"elbot/internal/fileops"
	"elbot/internal/operationresult"
	"elbot/internal/workspace"
)

const (
	fileToolDetailedMaxBytes = int64(fileops.MaxFileSize)
	fileToolMaxBytes         = 100 * 1024 * 1024
)

type ReadFileTool struct {
	FileGuard FileGuard
}

type EditFileTool struct {
	Rollback *fileops.Service
}

type readFileArgs struct {
	Path         string             `json:"path"`
	Encoding     string             `json:"encoding"`
	StartLine    readFileInteger    `json:"start_line"`
	EndLine      fileops.LineNumber `json:"end_line"`
	Mode         string             `json:"mode"`
	Query        string             `json:"query"`
	Glob         []string           `json:"glob"` // grep 目录搜索的 rg 路径过滤规则。
	ContextLines int                `json:"context_lines"`
	Index        readFileIndex      `json:"index"`
}

type readFileIndex struct {
	start, end int  // 匹配编号，1-based 闭区间。
	single     bool // 整数选择详情；范围选择列表。
}

func (p *readFileIndex) UnmarshalJSON(data []byte) error {
	invalid := fmt.Errorf("index must be a positive integer or a range like \"8-12\" (at most 100 entries)")
	if data[0] != '"' {
		var n int
		if err := json.Unmarshal(data, &n); err != nil || n < 1 {
			return invalid
		}
		*p = readFileIndex{start: n, end: n, single: true}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	a, b, ok := strings.Cut(s, "-")
	start, err1 := strconv.Atoi(a)
	end, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil || start < 1 || end < start || end-start >= 100 {
		return invalid
	}
	*p = readFileIndex{start: start, end: end}
	return nil
}

func (p readFileIndex) outOfRange(total int) bool {
	return p.start > total && (p.single || p.start > 1)
}

// exact 表示 total 是精确总数；否则只扫描到本页后的首个匹配。
func (p readFileIndex) summary(total int, exact bool) string {
	if total == 0 {
		return "0/0"
	}
	end := min(p.end, total)
	shown := strconv.Itoa(p.start)
	if p.start != end {
		shown += "-" + strconv.Itoa(end)
	}
	if total <= end {
		return fmt.Sprintf("%s/%d", shown, total)
	}
	denom := strconv.Itoa(total)
	nextEnd := end + 100
	if exact {
		nextEnd = min(nextEnd, total)
	} else {
		denom = strconv.Itoa(end) + "+"
	}
	return fmt.Sprintf("%s/%s · next index: \"%d-%d\"", shown, denom, end+1, nextEnd)
}

func readFileHeader(file fileops.File) string {
	header := fmt.Sprintf("%s | revision: %s", file.Path, fileops.ContentRevision(file.Bytes))
	if file.Encoding != "utf-8" {
		header += " | encoding: " + file.Encoding
	}
	return header
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

func NewReadFileTool(fileGuard FileGuard) ReadFileTool {
	return ReadFileTool{FileGuard: fileGuard}
}

func (t ReadFileTool) Definition() toolshare.ToolDefinition {
	definition := readFileBuilder().Build(t.PrepareCall)
	definition.Parameters["properties"].(map[string]any)["index"].(map[string]any)["type"] = []string{"string", "integer"}
	return definition
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
	return toolshare.NewBuilder("read_file").Weavable().
		Description("读文本→行号+revision；grep搜索文本；ast查看Go文件声明骨架，再按行范围read实现。").
		Risk(toolshare.RiskLow).
		SuperadminOnly().
		Tags("files").
		DependsOn("workspace", "rollback_file").
		String("path", "read/ast仅文件；grep可递归目录。", toolshare.Required()).
		String("encoding", "文件编码，默认auto。").
		String("mode", "默认read；grep文本搜索；ast仅Go单文件结构索引", toolshare.Enum("read", "grep", "ast")).
		String("query", "仅grep必填，逐行Go regex，首尾空白忽略。").
		StringArray("glob", "仅grep目录搜索；语法同rg -g，如[\"*.go\",\"!**/*_test.go\"]；省略/[]=不过滤。").
		String("index", `grep/ast：i or "i-j"。默认 100 条，一次最多100条。`).
		Integer("start_line", "read起始行，1-based，默认1。").
		String("end_line", "read结束行(含)，数字字符串或\"end\"；默认200行，end读至末尾。").
		Integer("context_lines", "仅grep上下文行数；0/省略→2，负数→0，上限20。")
}

func (t ReadFileTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	args := readFileArgs{Index: readFileIndex{start: 1, end: 100}}
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("parse read_file arguments: %v", err)}, nil
		}
	}
	mode, err := normalizeReadFileMode(args.Mode)
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	if len(args.Glob) > 0 && mode != readFileModeGrep {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: "glob requires grep directory search"}, nil
	}
	args.StartLine = readFileInteger(max(1, int(args.StartLine)))
	defaultEnd := !args.EndLine.End && args.EndLine.Value <= 0
	if defaultEnd {
		args.EndLine.Value = int(args.StartLine) + fileops.DefaultReadLineLimit - 1
	}
	var index any = args.Index.start
	if !args.Index.single {
		index = fmt.Sprintf("%d-%d", args.Index.start, args.Index.end)
	}
	var endLine any = args.EndLine.Value
	if args.EndLine.End {
		endLine = "end"
	}
	args.Query = strings.TrimSpace(args.Query)
	args.ContextLines = fileops.NormalizeGrepContextLines(args.ContextLines)
	if strings.TrimSpace(args.Encoding) == "" {
		args.Encoding = "auto"
	}
	selected, err := workspace.ResolveWorkspacePath(ctx, args.Path, workspace.PathResolveOptions{AllowDirectory: mode == readFileModeGrep})
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{}, err
	}
	if selected.Status != operationresult.Success {
		return operationresult.Result[*toolshare.PreparedCall]{Status: selected.Status, Message: selected.Message}, nil
	}
	resolved := selected.Data
	path := resolved.Path
	assessment := toolshare.RiskAssessment{Level: toolshare.RiskLow, Reasons: []string{"读取普通文本文件"}}
	if isSensitiveReadFile(path) {
		assessment = toolshare.RiskAssessment{Level: toolshare.RiskHigh, Reasons: []string{"读取可能包含凭据的敏感文件，需要用户确认"}}
	}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, assessment, []toolshare.ConfirmationDetailBlock{toolshare.FieldsConfirmationDetail(
		toolshare.ConfirmationField{Key: "path", Label: "读取路径", Value: path},
		toolshare.ConfirmationField{Key: "mode", Label: "模式", Value: mode},
		toolshare.ConfirmationField{Key: "encoding", Label: "编码", Value: args.Encoding},
		toolshare.ConfirmationField{Key: "start_line", Label: "起始行", Value: args.StartLine},
		toolshare.ConfirmationField{Key: "end_line", Label: "结束行", Value: endLine},
		toolshare.ConfirmationField{Key: "query", Label: "查询", Value: args.Query},
		toolshare.ConfirmationField{Key: "glob", Label: "路径过滤", Value: args.Glob},
		toolshare.ConfirmationField{Key: "context_lines", Label: "上下文行数", Value: args.ContextLines},
		toolshare.ConfirmationField{Key: "index", Label: "结果编号", Value: index},
	)}, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		var warnings []string
		if resolved.WasAbs {
			warning := toolshare.AbsolutePathWorkspaceWarning
			if resolved.IsWithinWorkspace() {
				warning = toolshare.AbsolutePathWithinWorkspaceWarning
			}
			warnings = append(warnings, warning)
		}
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			message := fmt.Sprintf("file %q not found", path)
			return operationresult.Result[*toolshare.Result]{Status: operationresult.NotFound, Message: message}, message, nil
		}
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		if len(args.Glob) > 0 && !info.IsDir() {
			message := "glob requires grep directory search"
			return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidArguments, Message: message}, message, nil
		}
		var result operationresult.Result[*toolshare.Result]
		if info.IsDir() {
			if mode != readFileModeGrep {
				message := fmt.Sprintf("%s mode requires a file path", mode)
				return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidArguments, Message: message}, message, nil
			}
			result, err = readFileDirectorySearch(ctx, path, args, warnings)
		} else {
			if info.Size() > fileToolMaxBytes {
				message := fmt.Sprintf("file too large: %d bytes exceeds %d", info.Size(), fileToolMaxBytes)
				return operationresult.Result[*toolshare.Result]{Status: operationresult.Rejected, Message: message}, message, nil
			}
			largeFile := info.Size() > fileToolDetailedMaxBytes
			if largeFile && mode == readFileModeAST {
				message := fmt.Sprintf("%s mode is disabled for this %d-byte file; use read or grep mode", mode, info.Size())
				return operationresult.Result[*toolshare.Result]{Status: operationresult.Rejected, Message: message}, message, nil
			}
			loaded, loadErr := fileops.ReadFileWithLimit(path, args.Encoding, fileToolMaxBytes)
			if loadErr != nil {
				return operationresult.Result[*toolshare.Result]{}, loadErr.Error(), loadErr
			}
			if loaded.Status != operationresult.Success {
				return operationresult.Result[*toolshare.Result]{Status: loaded.Status, Message: loaded.Message}, loaded.Message, nil
			}
			file := loaded.Data
			warnings = append(warnings, t.FileGuard.ReadWarnings(path)...)
			if largeFile {
				warnings = append(warnings, fmt.Sprintf("large file: %d bytes; ast mode is disabled", info.Size()))
			}
			switch mode {
			case readFileModeGrep:
				result, err = readFileGrepResult(ctx, file, args.Query, args.ContextLines, args.Index, warnings)
			case readFileModeAST:
				result = readFileASTResult(file, args.Index, warnings)
			case readFileModeRead:
				lines := fileops.SplitLines(file.Text)
				start, end, err := fileops.NormalizeReadRange(len(lines), int(args.StartLine), args.EndLine)
				if err != nil {
					return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidArguments, Message: err.Error()}, err.Error(), nil
				}
				var b strings.Builder
				fmt.Fprintf(&b, "%s | ", readFileHeader(file))
				if len(lines) == 0 {
					b.WriteString("lines: 0/0\n")
					result := &toolshare.Result{Content: b.String(), Warnings: warnings}
					return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, fmt.Sprintf("file=%s mode=%s lines=0/0 empty=true", file.Path, mode), nil
				}

				fmt.Fprintf(&b, "lines: %d-%d/%d\n", start, end, len(lines))
				width := len(fmt.Sprintf("%d", end))
				for i := start; i <= end; i++ {
					fmt.Fprintf(&b, "%*d | %s\n", width, i, lines[i-1])
				}
				result := &toolshare.Result{Content: b.String(), Warnings: warnings}
				return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, fmt.Sprintf("file=%s mode=%s lines=%d-%d/%d", file.Path, mode, start, end, len(lines)), nil
			}
		}
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, fmt.Sprintf("file=%s mode=%s", path, mode), err
		}
		return result, fmt.Sprintf("file=%s mode=%s", path, mode), nil
	})}, nil
}

func readFileGrepResult(ctx context.Context, file fileops.File, query string, contextLines int, index readFileIndex, warnings []string) (operationresult.Result[*toolshare.Result], error) {
	if query == "" {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidArguments, Message: "grep is required"}, nil
	}
	pattern, err := regexp.Compile(query)
	if err != nil {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("invalid grep regex: %v", err)}, nil
	}
	lines := fileops.SplitLines(file.Text)
	matches := make([]int, 0)
	total := 0
	for i, line := range lines {
		if err := ctx.Err(); err != nil {
			return operationresult.Result[*toolshare.Result]{}, err
		}
		if pattern.MatchString(line) {
			total++
			if total >= index.start && total <= index.end {
				matches = append(matches, i+1)
			}
			if total > index.end {
				break
			}
		}
	}
	if index.outOfRange(total) {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("index %d is out of range; found %d matches", index.start, total)}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s | grep: %q | matches: %s\n", readFileHeader(file), query, index.summary(total, false))
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
		fmt.Fprintf(&b, "%d. match: %d\n", matchIndex+index.start, matchLine)
		for i := start; i <= end; i++ {
			marker := " "
			if i == matchLine {
				marker = ">"
			}
			fmt.Fprintf(&b, "%s %*d | %s\n", marker, width, i, lines[i-1])
		}
		lastPrinted = end
	}
	return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Content: b.String(), Warnings: warnings}}, nil
}

func (t EditFileTool) Definition() toolshare.ToolDefinition {
	builder := editFileBuilder()

	schema := builder.BuildSchema()
	schema.Parameters["properties"].(map[string]any)["edits"].(map[string]any)["minItems"] = 1
	definition := builder.Build(t.PrepareCall)
	definition.Parameters = schema.Parameters

	return definition
}

func editFileBuilder() *toolshare.Builder {
	editProperties := fileops.EditOperationProperties()
	return toolshare.NewBuilder("edit_file").Weavable().
		Description("批量编辑单个文本文件；成功→unified diff，任一操作失败→不写入。").
		Risk(toolshare.RiskHigh).
		DependsOn("workspace", "rollback_file", "draft").
		Tags("files").
		String("path", "相对workspace或绝对路径。", toolshare.Required()).
		String("encoding", "默认auto；非UTF-8显式指定，如gb18030、big5、shift_jis。").
		String("expected_revision", "已有文件∧含replace/insert/delete/overwrite⇒必填；取read_file的revision。其他操作可传以校验版本；新建时省略。").
		Boolean("create", "true→允许新建；expected_revision非空→文件必须存在。").
		Integer("context_lines", "diff上下文行数；0/省略→3，负数→0，上限20。").
		ObjectArray("edits", "非空；定位基于编辑前原文。修改范围不重叠，插入点不在其内部；overwrite独占批次。", editProperties, []string{"operation"}, toolshare.Required())
}

func (t EditFileTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	var args editFileArgs
	if len(req.Arguments) > 0 {
		if err := decodeStrictFileArgs(req.Arguments, &args); err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("parse edit_file arguments: %v", err)}, nil
		}
	}
	prepared, err := t.Rollback.PrepareEdit(ctx, editServiceRequest(args))
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{}, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[*toolshare.PreparedCall]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	preparedEdit := prepared.Data
	details := editFileRiskDetails(args, preparedEdit.Preview())
	assessment := toolshare.RiskAssessment{Level: toolshare.RiskHigh, Reasons: []string{"文件内容写入操作需要确认"}}
	if scope, ok := workspace.PathScopeFromContext(ctx); ok && scope.Restricted {
		assessment = toolshare.RiskAssessment{Level: toolshare.RiskMedium, Reasons: []string{"文件编辑限制在当前调用工作目录内"}}
	}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, assessment, details, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		return t.call(ctx, preparedEdit)
	})}, nil
}

func editFileRiskDetails(args editFileArgs, preview fileops.EditResult) []toolshare.ConfirmationDetailBlock {
	return []toolshare.ConfirmationDetailBlock{
		toolshare.FieldsConfirmationDetail(
			toolshare.ConfirmationField{Key: "path", Label: "文件", Value: preview.Path},
			toolshare.ConfirmationField{Key: "created", Label: "创建文件", Value: preview.Created},
			toolshare.ConfirmationField{Key: "expected_revision", Label: "校验版本", Value: args.ExpectedRevision},
			toolshare.ConfirmationField{Key: "encoding", Label: "编码", Value: preview.Encoding},
		),
		toolshare.FileDiffConfirmationDetail(preview.Diff),
	}
}

func fileToolEditOptions() fileops.EditFileOptions {
	return fileops.EditFileOptions{
		MaxInputBytes:  fileToolMaxBytes,
		MaxOutputBytes: fileToolMaxBytes,
	}
}

func (t EditFileTool) call(ctx context.Context, prepared *fileops.PreparedEdit) (operationresult.Result[*toolshare.Result], string, error) {
	lease := t.Rollback.EditSession(ctx)
	if lease.Status != operationresult.Success {
		return operationresult.Result[*toolshare.Result]{Status: lease.Status, Message: lease.Message}, lease.Message, nil
	}
	committed, resolved, err := prepared.Commit(ctx)
	if err != nil {
		return operationresult.Result[*toolshare.Result]{}, err.Error(), err
	}
	if committed.Status != operationresult.Success {
		return operationresult.Result[*toolshare.Result]{Status: committed.Status, Message: committed.Message}, "file edit rejected: " + committed.Message, nil
	}
	result := committed.Data
	content := fmt.Sprintf("edited: %s | encoding: %s | revision_after: %s\ndiff:\n%s", result.Path, result.Encoding, result.RevisionAfter, result.Diff)
	if result.Created {
		content = "created: true | " + content
	}
	if result.DryRun {
		content = "dry_run: true | " + content
	}
	content += fmt.Sprintf("\nrollback_available: %t", result.RollbackAvailable)
	var warnings []string
	if resolved.WasAbs {
		warning := toolshare.AbsolutePathWorkspaceWarning
		if resolved.IsWithinWorkspace() {
			warning = toolshare.AbsolutePathWithinWorkspaceWarning
		}
		warnings = append(warnings, warning)
	}
	if result.RollbackEvicted > 0 {
		warnings = append(warnings, fmt.Sprintf("撤销备份内存已满；已删最旧 %d 条。", result.RollbackEvicted))
	}
	if lease.Data != nil && !result.RollbackAvailable {
		warnings = append(warnings, "已编辑，无撤销备份（会话失效或容量受限）。")
	}
	return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Content: content, Warnings: warnings}}, fmt.Sprintf("edited=%s created=%t dry_run=%t rollback_available=%t", result.Path, result.Created, result.DryRun, result.RollbackAvailable), nil
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
