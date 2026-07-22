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
		"operation":   map[string]any{"type": "string", "enum": []string{"replace_text", "replace", "insert", "delete", "insert_before", "insert_after", "replace_line", "delete_line", "overwrite"}, "description": "编辑操作选择：replace_text 通过 old_text 精确定位并替换，可跨行；replace 通过 line/end_line 替换指定行范围，new_text 原样写入且不自动补换行，若需保留换行必须手动传入 \\n；replace_line 通过 anchor 匹配并替换一整行，缩进需手动，换行自动追加。所有目标都基于编辑前原文解析；overwrite 必须是批次中的唯一操作。"},
		"line":        map[string]any{"type": "integer", "description": "replace/delete 的起始行或 insert 的行间位置，1-based。insert: 1=文件开头，N+1=文件末尾；空文件仅支持 1。"},
		"end_line":    map[string]any{"type": "integer", "description": "replace/delete 的可选结束行，1-based 且包含该行；省略时只操作 line。"},
		"new_text":    map[string]any{"type": "string", "description": "写入文本。replace_text/replace/overwrite 使用原样文本；replace 不自动补换行，若需换行，需手动添加。insert/insert_before/insert_after/replace_line 把内容作为完整行块，缩进需手动，换行自动追加。replace_text 和 replace 可传空字符串进行删除。"},
		"old_text":    map[string]any{"type": "string", "description": "replace_text 的精确单行或多行匹配文本。"},
		"anchor":      map[string]any{"type": "string", "description": "insert_before/insert_after/replace_line/delete_line 的单行前缀；匹配时忽略目标行的前导空格和 Tab。匹配多行但没传入index时，会返回index"},
		"index":       map[string]any{"type": "integer", "description": "匹配到多处时选择第几处，1-based。与 all_matches 互斥。"},
		"all_matches": map[string]any{"type": "boolean", "description": "对所有匹配执行操作。与 index 互斥；仅用于 old_text/anchor 操作。"},
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
