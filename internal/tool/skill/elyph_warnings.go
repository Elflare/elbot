package skill

import (
	"strings"

	"elbot/internal/elyph"
	"elbot/internal/tool"
)

func appendElyphWarnings(content string, diagnostics []elyph.Diagnostic) string {
	warnings := elyph.WarningDiagnostics(diagnostics)
	if len(warnings) == 0 {
		return content
	}
	return tool.AppendWarnings(content, []string{strings.TrimSpace(elyph.FormatDiagnostics(warnings))})
}
