package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"elbot/internal/llm"
	"elbot/internal/tool"
)

type CommandTool struct {
	Record   Record
	Manifest AgentSkillManifest
}

func NewCommandTool(record Record) CommandTool {
	return CommandTool{Record: record, Manifest: record.Manifest}
}

func (t CommandTool) Name() string { return t.Record.Name }

func (t CommandTool) Info() tool.Info {
	return tool.Info{Name: t.Record.Name, Description: t.Record.Description, Source: SourceForKind(t.Record.Kind), Risk: t.Manifest.Risk, SuperadminOnly: t.Manifest.SuperadminOnly, Tags: t.Manifest.Tags}
}

func (t CommandTool) Schema() llm.ToolSchema {
	return t.Manifest.Schema(t.Record.Name, t.Record.Description)
}

func (t CommandTool) Detail() string {
	return tool.RenderDetailBlocks([]tool.DetailBlock{t.DetailBlock()})
}

func (t CommandTool) DetailBlock() tool.DetailBlock {
	content := t.Record.Detail
	if t.Manifest.ExposeRoot {
		content = strings.TrimSpace(content) + "\n\nAgentSkill root: " + t.Record.Root
	}
	return tool.DetailBlock{Content: strings.TrimSpace(content), Format: t.Record.Format}
}

func (t CommandTool) ActivateTools() []string {
	return []string{AgentSkillManagerName, t.Record.Name}
}

func (t CommandTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	args, err := commandArguments(t.Manifest, json.RawMessage(req.Arguments))
	if err != nil {
		return nil, err
	}
	command := append([]string(nil), t.Manifest.Command...)
	command = append(command, args...)
	timeout := defaultRunnerTimeout
	if t.Manifest.TimeoutSeconds > 0 {
		timeout = time.Duration(t.Manifest.TimeoutSeconds) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, command[0], command[1:]...)
	cmd.Dir = t.Record.Root
	return runCommand(runCtx, "AgentSkill command", cmd)
}

func commandArguments(manifest AgentSkillManifest, raw json.RawMessage) ([]string, error) {
	values := map[string]json.RawMessage{}
	if len(raw) > 0 && strings.TrimSpace(string(raw)) != "" {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("parse skill tool arguments: %w", err)
		}
	}
	for _, name := range schemaRequired(manifest.Parameters) {
		if _, ok := values[name]; !ok {
			return nil, fmt.Errorf("required argument %s is missing", name)
		}
	}
	names := make([]string, 0, len(manifest.Args))
	for name := range manifest.Args {
		names = append(names, name)
	}
	sort.Strings(names)
	out := []string{}
	for _, name := range names {
		value, ok := values[name]
		if !ok || string(value) == "null" {
			continue
		}
		text, err := argumentText(value)
		if err != nil {
			return nil, fmt.Errorf("argument %s: %w", name, err)
		}
		out = append(out, manifest.Args[name], text)
	}
	return out, nil
}

func argumentText(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err == nil {
		return number.String(), nil
	}
	var boolean bool
	if err := json.Unmarshal(raw, &boolean); err == nil {
		if boolean {
			return "true", nil
		}
		return "false", nil
	}
	return "", fmt.Errorf("must be string, number, or boolean")
}
