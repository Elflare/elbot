package skill

import (
	"strings"

	"elbot/internal/elyph"
)

func appendElyphWarnings(content string, diagnostics []elyph.Diagnostic) string {
	warnings := elyph.WarningDiagnostics(diagnostics)
	if len(warnings) == 0 {
		return content
	}
	return strings.TrimRight(content, "\n") + "\n\nWarnings:\n" + elyph.FormatDiagnostics(warnings)
}
