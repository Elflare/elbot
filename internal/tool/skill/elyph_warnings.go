// 组织 Skill 的 ELyph 检查警告。
package skill

import toolshare "elbot/internal/tool/share"

import (
	"strings"

	"elbot/internal/elyph"
)

func appendElyphWarnings(content string, diagnostics []elyph.Diagnostic) string {
	warnings := elyph.WarningDiagnostics(diagnostics)
	if len(warnings) == 0 {
		return content
	}
	return toolshare.AppendWarnings(content, []string{strings.TrimSpace(elyph.FormatDiagnostics(warnings))})
}
