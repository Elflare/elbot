package toolrun

import (
	"fmt"
	"regexp"
	"strings"

	"elbot/internal/llm"
)

var elwispToolNameReplacer = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

type ELwispToolDeclaration struct {
	Name           string         `json:"name"`
	Description    string         `json:"description,omitempty"`
	Schema         map[string]any `json:"schema,omitempty"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
	Endpoint       string         `json:"endpoint,omitempty"`
	ForegroundOnly bool           `json:"foreground_only,omitempty"`
}

type ELwispInjection struct {
	ELwispName string
	EventKey   string
	Tools      []ELwispToolDeclaration
}

func CachedToolsFromELwisp(injection ELwispInjection) []CachedTool {
	elwispName := strings.TrimSpace(injection.ELwispName)
	if elwispName == "" {
		return nil
	}
	out := make([]CachedTool, 0, len(injection.Tools))
	for _, declared := range injection.Tools {
		name := strings.TrimSpace(declared.Name)
		if name == "" {
			continue
		}
		canonical := elwispToolName(elwispName, name)
		out = append(out, CachedTool{
			Name:           name,
			CanonicalName:  canonical,
			Source:         SourceKindELwisp,
			Description:    declared.Description,
			Schema:         llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{Name: canonical, Description: declared.Description, Parameters: declared.Schema}},
			ELwispName:     elwispName,
			EventKey:       injection.EventKey,
			Endpoint:       strings.TrimSpace(declared.Endpoint),
			TimeoutSeconds: declared.TimeoutSeconds,
			ForegroundOnly: declared.ForegroundOnly,
		})
	}
	return NormalizeCachedTools(out)
}

func elwispToolName(elwispName, name string) string {
	elwispName = elwispToolNameReplacer.ReplaceAllString(strings.TrimSpace(elwispName), "_")
	name = elwispToolNameReplacer.ReplaceAllString(strings.TrimSpace(name), "_")
	return fmt.Sprintf("elwisp_%s_%s", elwispName, name)
}
