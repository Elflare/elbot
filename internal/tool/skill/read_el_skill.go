// 实现原生 Skill 读取工具，读取并展示技能文档或 Go 源码的指定行范围。
package skill

import (
	"context"
	"elbot/internal/fileops"
	"elbot/internal/operationresult"
	toolshare "elbot/internal/tool/share"
	"encoding/json"
	"fmt"
	"strings"
)

const ReadElSkillName = "read_el_skill"

type ReadElSkillTool struct {
	manager *Manager
}

type readElSkillArgs struct {
	Name      string `json:"name"`
	Target    string `json:"target"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

func (t ReadElSkillTool) Definition() toolshare.ToolDefinition {
	builder := toolshare.NewBuilder(ReadElSkillName).
		Description("读EL Skill文件→1-based行号+revision；省略行范围→全文。").
		DependsOn("modify_el_skill").
		Risk(toolshare.RiskMedium)

	definition := builder.Build(t.PrepareCall)
	definition.Parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":       map[string]any{"type": "string", "description": "Skill名称。"},
			"target":     map[string]any{"type": "string", "enum": []string{elSkillTargetElyph, elSkillTargetCodeSource}, "description": "skill_elyph→SKILL.elyph(默认)，code_source→main.go。"},
			"start_line": map[string]any{"type": "integer", "description": "起始行，默认1。"},
			"end_line":   map[string]any{"type": "integer", "description": "结束行(含)，默认末尾。"},
		},
		"required": []string{"name"},
	}
	return definition
}

func (t ReadElSkillTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	var args readElSkillArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("parse read_el_skill arguments: %v", err)}, nil
		}
	}
	selected, target, err := elSkillTargetPath(t.manager, strings.TrimSpace(args.Name), strings.TrimSpace(args.Target))
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{}, err
	}
	if selected.Status != operationresult.Success {
		return operationresult.Result[*toolshare.PreparedCall]{Status: selected.Status, Message: selected.Message}, nil
	}
	path := selected.Data
	details := []toolshare.ConfirmationDetailBlock{toolshare.TextConfirmationDetail(fmt.Sprintf("读取 Skill：%s\n文件：%s", args.Name, path))}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, details, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		loaded, err := fileops.ReadFile(path, "")
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		if loaded.Status != operationresult.Success {
			return operationresult.Result[*toolshare.Result]{Status: loaded.Status, Message: loaded.Message}, loaded.Message, nil
		}
		file := loaded.Data
		lines := fileops.SplitLines(file.Text)
		start := args.StartLine
		if start <= 0 {
			start = 1
		}
		end := args.EndLine
		if end <= 0 || end > len(lines) {
			end = len(lines)
		}
		if start > end+1 || start > len(lines)+1 {
			{
				message := fmt.Sprintf("start_line %d is out of range", args.StartLine)
				return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidArguments, Message: message}, message, nil
			}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "revision: %s\n", fileops.ContentRevision(file.Bytes))
		for i := start; i <= end; i++ {
			fmt.Fprintf(&b, "%d: %s\n", i, lines[i-1])
		}
		{
			result := &toolshare.Result{Content: strings.TrimRight(b.String(), "\n"), NeedsElyphRules: target == elSkillTargetElyph}
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, fmt.Sprintf("skill=%s target=%s lines=%d-%d/%d", args.Name, args.Target, start, end, len(lines)), nil
		}
	})}, nil
}
