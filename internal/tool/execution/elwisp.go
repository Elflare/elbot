// 解析 Elwisp 工具声明并转换调用描述。
package execution

import toolshare "elbot/internal/tool/share"

import (
	"fmt"
	"regexp"
	"strings"

	"elbot/internal/llm"
)

var elwispToolNameReplacer = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func CachedToolsFromELwisp(injection toolshare.ELwispInjection) []toolshare.DiscoveredTool {
	elwispName := strings.TrimSpace(injection.ELwispName)
	if elwispName == "" {
		return nil
	}
	out := make([]toolshare.DiscoveredTool, 0, len(injection.Tools))
	for _, declared := range injection.Tools {
		name := strings.TrimSpace(declared.Name)
		if name == "" {
			continue
		}
		canonical := elwispToolName(elwispName, name)
		out = append(out, toolshare.DiscoveredTool{
			Name:           name,
			CanonicalName:  canonical,
			Source:         toolshare.SourceKindELwisp,
			Description:    declared.Description,
			Schema:         llm.ToolSchema{Name: canonical, Description: declared.Description, Parameters: declared.Schema},
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
