package fileops

import (
	"fmt"
	"sort"
	"strings"
)

type Edit struct {
	Operation  string  `json:"operation"`
	Line       int     `json:"line"`
	EndLine    int     `json:"end_line"`
	NewText    *string `json:"new_text"`
	OldText    string  `json:"old_text"`
	Anchor     string  `json:"anchor"`
	Index      *int    `json:"index"`
	AllMatches bool    `json:"all_matches"`
}

func EditOperationProperties() map[string]any {
	return map[string]any{
		"operation": map[string]any{"type": "string", "enum": []string{"replace_text", "replace", "insert", "delete", "insert_before", "insert_after", "replace_line", "delete_line", "overwrite"}, "description": `所需参数([ ]=可省)：
replace_text(old_text,new_text)：精确文本替换，可跨行；
replace(line,[end_line],new_text)/delete(line,[end_line])：替换/删除行范围；
insert(line,new_text)：按行位置插入；
insert_before/insert_after(anchor,new_text)：匹配行前/后插入；
replace_line(anchor,new_text)/delete_line(anchor)：替换/删除匹配整行；
overwrite(new_text)：替换全文，必须独占批次。`},
		"line":        map[string]any{"type": "integer", "description": "1-based；replace/delete起始行；insert位置：1=开头，N+1=末尾，空文件仅1。"},
		"end_line":    map[string]any{"type": "integer", "description": "replace/delete结束行(含)，默认line。"},
		"new_text":    map[string]any{"type": "string", "description": "replace_text/replace/overwrite不补换行，允许\"\"；insert/insert_before/insert_after/replace_line按整行块处理，行分隔自动、缩进自带；replace_line保留原行终止符。"},
		"old_text":    map[string]any{"type": "string", "description": "非空精确匹配文本，可跨行。"},
		"anchor":      map[string]any{"type": "string", "description": "非空单行前缀，不带缩进；匹配目标行去除前导空格/Tab后的文本。"},
		"index":       map[string]any{"type": "integer", "minimum": 1, "description": "old_text/anchor的第几个匹配，1-based；与all_matches=true互斥。"},
		"all_matches": map[string]any{"type": "boolean", "description": "true→全部old_text/anchor匹配；默认false。多处匹配且未选index/true→报错并返回候选编号。"},
	}
}

type editSourceLine struct {
	start   int
	end     int
	fullEnd int
	text    string
}

type resolvedEdit struct {
	start int
	end   int
	text  string
	order int
}

func editsRequireRevision(edits []Edit) bool {
	for _, edit := range edits {
		switch strings.ToLower(strings.TrimSpace(edit.Operation)) {
		case "replace", "insert", "delete", "overwrite":
			return true
		}
	}
	return false
}

func ApplyEdits(text string, edits []Edit) (string, error) {
	if len(edits) == 0 {
		return "", fmt.Errorf("edits is required")
	}
	text = NormalizeEditText(text)
	if strings.EqualFold(strings.TrimSpace(edits[0].Operation), "overwrite") {
		if len(edits) != 1 {
			return "", fmt.Errorf("edit 1: overwrite must be the only edit")
		}
		if err := validateEditFields(edits[0], "overwrite"); err != nil {
			return "", fmt.Errorf("edit 1: %w", err)
		}
		return NormalizeEditText(*edits[0].NewText), nil
	}

	lines := scanEditLines(text)
	resolved := make([]resolvedEdit, 0, len(edits))
	for i, edit := range edits {
		operation := strings.ToLower(strings.TrimSpace(edit.Operation))
		if operation == "overwrite" {
			return "", fmt.Errorf("edit %d: overwrite must be the only edit", i+1)
		}
		items, err := resolveEdit(text, lines, edit, operation)
		if err != nil {
			return "", fmt.Errorf("edit %d: %w", i+1, err)
		}
		for _, item := range items {
			item.order = len(resolved)
			resolved = append(resolved, item)
		}
	}
	if err := validateResolvedEdits(resolved); err != nil {
		return "", err
	}
	sort.SliceStable(resolved, func(i, j int) bool {
		if resolved[i].start != resolved[j].start {
			return resolved[i].start > resolved[j].start
		}
		iInsert := resolved[i].start == resolved[i].end
		jInsert := resolved[j].start == resolved[j].end
		if iInsert != jInsert {
			return !iInsert
		}
		return resolved[i].order > resolved[j].order
	})
	out := text
	for _, edit := range resolved {
		out = out[:edit.start] + edit.text + out[edit.end:]
	}
	return out, nil
}

func resolveEdit(text string, lines []editSourceLine, edit Edit, operation string) ([]resolvedEdit, error) {
	if err := validateEditFields(edit, operation); err != nil {
		return nil, err
	}
	switch operation {
	case "replace_text":
		oldText := NormalizeEditText(edit.OldText)
		locations, err := selectMatches(findContentMatches(text, oldText), edit.Index, edit.AllMatches, "old_text")
		if err != nil {
			return nil, err
		}
		out := make([]resolvedEdit, 0, len(locations))
		for _, loc := range locations {
			out = append(out, resolvedEdit{start: loc.byteStart, end: loc.byteEnd, text: NormalizeEditText(*edit.NewText)})
		}
		return out, nil
	case "replace":
		endLine := edit.EndLine
		if endLine == 0 {
			endLine = edit.Line
		}
		if err := ValidateLineRange(len(lines), edit.Line, endLine); err != nil {
			return nil, err
		}
		start, end := lineDeleteRange(lines, edit.Line, endLine)
		return []resolvedEdit{{start: start, end: end, text: NormalizeEditText(*edit.NewText)}}, nil
	case "insert":
		block, err := normalizeLineBlock(*edit.NewText)
		if err != nil {
			return nil, err
		}
		start, inserted, err := resolveLineInsert(text, lines, edit.Line, block)
		if err != nil {
			return nil, err
		}
		return []resolvedEdit{{start: start, end: start, text: inserted}}, nil
	case "delete":
		endLine := edit.EndLine
		if endLine == 0 {
			endLine = edit.Line
		}
		if err := ValidateLineRange(len(lines), edit.Line, endLine); err != nil {
			return nil, err
		}
		start, end := lineDeleteRange(lines, edit.Line, endLine)
		return []resolvedEdit{{start: start, end: end}}, nil
	case "insert_before", "insert_after", "replace_line", "delete_line":
		anchor := NormalizeEditText(edit.Anchor)
		locations, err := selectMatches(findLineMatches(lines, anchor), edit.Index, edit.AllMatches, "anchor")
		if err != nil {
			return nil, err
		}
		var block string
		if operation != "delete_line" {
			block, err = normalizeLineBlock(*edit.NewText)
			if err != nil {
				return nil, err
			}
		}
		out := make([]resolvedEdit, 0, len(locations))
		for _, loc := range locations {
			line := lines[loc.lineIndex]
			switch operation {
			case "insert_before":
				out = append(out, resolvedEdit{start: line.start, end: line.start, text: block + "\n"})
			case "insert_after":
				if line.fullEnd > line.end {
					out = append(out, resolvedEdit{start: line.fullEnd, end: line.fullEnd, text: block + "\n"})
				} else {
					out = append(out, resolvedEdit{start: line.end, end: line.end, text: "\n" + block})
				}
			case "replace_line":
				out = append(out, resolvedEdit{start: line.start, end: line.end, text: block})
			case "delete_line":
				start, end := lineDeleteRange(lines, loc.lineIndex+1, loc.lineIndex+1)
				out = append(out, resolvedEdit{start: start, end: end})
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported operation %q", edit.Operation)
	}
}

func validateEditFields(edit Edit, operation string) error {
	if operation == "" {
		return fmt.Errorf("operation is required")
	}
	if edit.Index != nil && edit.AllMatches {
		return fmt.Errorf("index and all_matches are mutually exclusive")
	}
	rejectLineFields := func() error {
		if edit.Line != 0 || edit.EndLine != 0 {
			return fmt.Errorf("operation %q does not use line or end_line", operation)
		}
		return nil
	}
	rejectMatchFields := func() error {
		if edit.Index != nil || edit.AllMatches {
			return fmt.Errorf("operation %q does not support index or all_matches", operation)
		}
		return nil
	}
	switch operation {
	case "replace_text":
		if NormalizeEditText(edit.OldText) == "" {
			return fmt.Errorf("old_text is required")
		}
		if edit.NewText == nil {
			return fmt.Errorf("new_text is required")
		}
		if strings.TrimSpace(edit.Anchor) != "" {
			return fmt.Errorf("operation %q uses old_text, not anchor", operation)
		}
		return rejectLineFields()
	case "replace":
		if edit.Line < 1 {
			return fmt.Errorf("line must be >= 1")
		}
		if edit.NewText == nil {
			return fmt.Errorf("new_text is required")
		}
		if edit.OldText != "" || edit.Anchor != "" {
			return fmt.Errorf("operation %q only uses line, optional end_line, and new_text", operation)
		}
		return rejectMatchFields()
	case "insert":
		if edit.Line < 1 {
			return fmt.Errorf("line must be >= 1")
		}
		if edit.EndLine != 0 || edit.OldText != "" || edit.Anchor != "" {
			return fmt.Errorf("operation %q only uses line and new_text", operation)
		}
		if edit.NewText == nil {
			return fmt.Errorf("new_text is required")
		}
		return rejectMatchFields()
	case "delete":
		if edit.Line < 1 {
			return fmt.Errorf("line must be >= 1")
		}
		if edit.NewText != nil || edit.OldText != "" || edit.Anchor != "" {
			return fmt.Errorf("operation %q only uses line and optional end_line", operation)
		}
		return rejectMatchFields()
	case "insert_before", "insert_after", "replace_line", "delete_line":
		anchor := NormalizeEditText(edit.Anchor)
		if anchor == "" {
			return fmt.Errorf("anchor is required")
		}
		if strings.Contains(anchor, "\n") {
			return fmt.Errorf("anchor must be a single-line prefix")
		}
		if edit.OldText != "" {
			return fmt.Errorf("operation %q uses anchor, not old_text", operation)
		}
		if err := rejectLineFields(); err != nil {
			return err
		}
		if operation == "delete_line" {
			if edit.NewText != nil {
				return fmt.Errorf("operation %q does not use new_text", operation)
			}
		} else if edit.NewText == nil {
			return fmt.Errorf("new_text is required")
		}
		return nil
	case "overwrite":
		if edit.NewText == nil {
			return fmt.Errorf("new_text is required")
		}
		if edit.Line != 0 || edit.EndLine != 0 || edit.OldText != "" || edit.Anchor != "" || edit.Index != nil || edit.AllMatches {
			return fmt.Errorf("operation %q only uses new_text", operation)
		}
		return nil
	default:
		return fmt.Errorf("unsupported operation %q", edit.Operation)
	}
}

func scanEditLines(text string) []editSourceLine {
	if text == "" {
		return nil
	}
	lines := make([]editSourceLine, 0, strings.Count(text, "\n")+1)
	for start := 0; start < len(text); {
		rel := strings.IndexByte(text[start:], '\n')
		if rel < 0 {
			lines = append(lines, editSourceLine{start: start, end: len(text), fullEnd: len(text), text: text[start:]})
			break
		}
		end := start + rel
		lines = append(lines, editSourceLine{start: start, end: end, fullEnd: end + 1, text: text[start:end]})
		start = end + 1
	}
	return lines
}

func normalizeLineBlock(text string) (string, error) {
	lines := SplitLines(EnsureTrailingNewline(text))
	if len(lines) == 0 {
		return "", fmt.Errorf("new_text must contain at least one line")
	}
	return strings.Join(lines, "\n"), nil
}

func resolveLineInsert(text string, lines []editSourceLine, line int, block string) (int, string, error) {
	total := len(lines)
	if line < 1 || line > total+1 {
		return 0, "", fmt.Errorf("insert line %d exceeds valid positions 1-%d", line, total+1)
	}
	if total == 0 {
		return 0, block, nil
	}
	if line <= total {
		return lines[line-1].start, block + "\n", nil
	}
	if strings.HasSuffix(text, "\n") {
		return len(text), block + "\n", nil
	}
	return len(text), "\n" + block, nil
}

func lineDeleteRange(lines []editSourceLine, startLine, endLine int) (int, int) {
	start := lines[startLine-1].start
	end := lines[endLine-1].fullEnd
	last := lines[endLine-1]
	if endLine == len(lines) && last.fullEnd == last.end && startLine > 1 {
		start = lines[startLine-2].end
	}
	return start, end
}

func validateResolvedEdits(edits []resolvedEdit) error {
	for i := 0; i < len(edits); i++ {
		for j := i + 1; j < len(edits); j++ {
			a, b := edits[i], edits[j]
			aInsert := a.start == a.end
			bInsert := b.start == b.end
			switch {
			case !aInsert && !bInsert && a.start < b.end && b.start < a.end:
				return fmt.Errorf("edits overlap in original content at byte ranges %d-%d and %d-%d", a.start, a.end, b.start, b.end)
			case aInsert && !bInsert && a.start > b.start && a.start < b.end:
				return fmt.Errorf("insertion at byte %d falls inside edited range %d-%d", a.start, b.start, b.end)
			case bInsert && !aInsert && b.start > a.start && b.start < a.end:
				return fmt.Errorf("insertion at byte %d falls inside edited range %d-%d", b.start, a.start, a.end)
			}
		}
	}
	return nil
}
