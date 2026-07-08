package skill

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/tool"

	"github.com/pelletier/go-toml/v2"
)

const AgentSkillConfigFile = "ELBOT_SKILL.toml"

type AgentSkillManifest struct {
	Risk           tool.RiskLevel
	SuperadminOnly bool
	Tags           []string
	Command        []string
	TimeoutSeconds int
	ExposeRoot     bool
	Parameters     map[string]any
	Args           map[string]string
	Callable       bool
}

type agentSkillManifestFile struct {
	Risk           string            `toml:"risk"`
	SuperadminOnly bool              `toml:"superadmin_only"`
	Tags           []string          `toml:"tags"`
	Command        []string          `toml:"command"`
	TimeoutSeconds int               `toml:"timeout_seconds"`
	ExposeRoot     bool              `toml:"expose_root"`
	Parameters     string            `toml:"parameters"`
	Args           map[string]string `toml:"args"`
}

func AgentSkillConfigPath(root string) string {
	return filepath.Join(root, AgentSkillConfigFile)
}

func LoadAgentSkillManifest(root string) (AgentSkillManifest, bool, error) {
	path := AgentSkillConfigPath(root)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return AgentSkillManifest{}, false, nil
	}
	if err != nil {
		return AgentSkillManifest{}, true, fmt.Errorf("read %s: %w", AgentSkillConfigFile, err)
	}
	manifest, err := ParseAgentSkillManifest(data)
	if err != nil {
		return AgentSkillManifest{}, true, err
	}
	return manifest, true, nil
}

func ParseAgentSkillManifest(data []byte) (AgentSkillManifest, error) {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return AgentSkillManifest{}, fmt.Errorf("parse %s: %w", AgentSkillConfigFile, err)
	}
	allowed := map[string]bool{"risk": true, "superadmin_only": true, "tags": true, "command": true, "timeout_seconds": true, "expose_root": true, "parameters": true, "args": true}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		if !allowed[key] {
			keys = append(keys, key)
		}
	}
	if len(keys) > 0 {
		sort.Strings(keys)
		return AgentSkillManifest{}, fmt.Errorf("unknown %s field(s): %s", AgentSkillConfigFile, strings.Join(keys, ", "))
	}

	var file agentSkillManifestFile
	if err := toml.Unmarshal(data, &file); err != nil {
		return AgentSkillManifest{}, fmt.Errorf("parse %s: %w", AgentSkillConfigFile, err)
	}
	return validateAgentSkillManifest(file)
}

func validateAgentSkillManifest(file agentSkillManifestFile) (AgentSkillManifest, error) {
	callable := agentSkillManifestCallable(file)
	risk := tool.RiskSafe
	if strings.TrimSpace(file.Risk) != "" {
		parsed, err := parseRisk(file.Risk)
		if err != nil {
			return AgentSkillManifest{}, err
		}
		risk = parsed
	} else if callable {
		return AgentSkillManifest{}, fmt.Errorf("risk is required")
	}
	if !callable {
		if file.TimeoutSeconds < 0 {
			return AgentSkillManifest{}, fmt.Errorf("timeout_seconds must be >= 0")
		}
		return AgentSkillManifest{Risk: risk, SuperadminOnly: file.SuperadminOnly, Tags: normalizeManifestTags(file.Tags), TimeoutSeconds: file.TimeoutSeconds, ExposeRoot: file.ExposeRoot}, nil
	}
	if len(file.Command) == 0 {
		return AgentSkillManifest{}, fmt.Errorf("command is required")
	}
	for i, part := range file.Command {
		if strings.TrimSpace(part) == "" {
			return AgentSkillManifest{}, fmt.Errorf("command[%d] is empty", i)
		}
	}
	if strings.TrimSpace(file.Parameters) == "" {
		return AgentSkillManifest{}, fmt.Errorf("parameters is required")
	}
	parameters := map[string]any{}
	if err := json.Unmarshal([]byte(file.Parameters), &parameters); err != nil {
		return AgentSkillManifest{}, fmt.Errorf("parameters must be JSON object schema: %w", err)
	}
	if typ, _ := parameters["type"].(string); typ != "object" {
		return AgentSkillManifest{}, fmt.Errorf("parameters.type must be object")
	}
	properties, ok := parameters["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		return AgentSkillManifest{}, fmt.Errorf("parameters.properties is required")
	}
	if len(file.Args) == 0 {
		return AgentSkillManifest{}, fmt.Errorf("[args] is required")
	}
	for name, flag := range file.Args {
		name = strings.TrimSpace(name)
		if name == "" {
			return AgentSkillManifest{}, fmt.Errorf("args contains empty parameter name")
		}
		if strings.TrimSpace(flag) == "" {
			return AgentSkillManifest{}, fmt.Errorf("args.%s is empty", name)
		}
		if _, ok := properties[name]; !ok {
			return AgentSkillManifest{}, fmt.Errorf("args.%s is not defined in parameters.properties", name)
		}
	}
	for _, name := range schemaRequired(parameters) {
		if _, ok := file.Args[name]; !ok {
			return AgentSkillManifest{}, fmt.Errorf("required parameter %s has no args mapping", name)
		}
	}
	if file.TimeoutSeconds < 0 {
		return AgentSkillManifest{}, fmt.Errorf("timeout_seconds must be >= 0")
	}
	return AgentSkillManifest{Risk: risk, SuperadminOnly: file.SuperadminOnly, Tags: normalizeManifestTags(file.Tags), Command: append([]string(nil), file.Command...), TimeoutSeconds: file.TimeoutSeconds, ExposeRoot: file.ExposeRoot, Parameters: parameters, Args: copyStringMap(file.Args), Callable: true}, nil
}

func agentSkillManifestCallable(file agentSkillManifestFile) bool {
	return len(file.Command) > 0 || strings.TrimSpace(file.Parameters) != "" || len(file.Args) > 0
}

func schemaRequired(parameters map[string]any) []string {
	value, ok := parameters["required"]
	if !ok {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		name, ok := item.(string)
		if ok && name != "" {
			out = append(out, name)
		}
	}
	return out
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func normalizeManifestTags(tags []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

func (m AgentSkillManifest) Schema(name, description string) llm.ToolSchema {
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{Name: name, Description: description, Parameters: m.Parameters}}
}
