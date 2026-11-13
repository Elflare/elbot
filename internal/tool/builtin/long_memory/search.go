// 检索长期记忆，处理分类查询、分词及结果展示。
package long_memory

import (
	"context"
	"elbot/internal/fileops"
	"elbot/internal/operationresult"
	"fmt"
	"strings"
	"unicode"
)

const (
	longMemoryDefaultLimit = 3
	longMemoryMaxLimit     = 10
)

func (s *longMemoryStore) search(ctx context.Context, args longMemorySearchArgs) (operationresult.Result[string], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[string]{}, err
	}
	keywords := strings.TrimSpace(args.Keywords)
	category := cleanLongMemoryText(args.Category)
	if keywords == "" && category == "" {
		return operationresult.Result[string]{Status: operationresult.InvalidArguments, Message: "keywords 和 category 不能同时为空"}, nil
	}
	briefOnly := true
	if args.BriefOnly != nil {
		briefOnly = *args.BriefOnly
	}
	limit := normalizeLongMemoryLimit(args.Limit)
	matchMode := normalizeLongMemoryMatchMode(args.MatchMode)

	s.mu.Lock()
	defer s.mu.Unlock()
	ready, err := s.ensureLocked(ctx)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if ready.Status != operationresult.Success {
		return operationresult.Result[string]{Status: ready.Status, Message: ready.Message}, nil
	}
	if err := s.syncLocked(ctx, false); err != nil {
		return operationresult.Result[string]{}, err
	}
	invalids, err := s.invalidFilesLocked(ctx, 5)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	overview, err := s.categoryOverviewLocked(ctx)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	records, err := s.searchFTSLocked(ctx, keywords, category, matchMode, limit)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	return formatSearchResult(invalids, overview, records, briefOnly)
}

func (s *longMemoryStore) categories(ctx context.Context) (operationresult.Result[string], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[string]{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ready, err := s.ensureLocked(ctx)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if ready.Status != operationresult.Success {
		return operationresult.Result[string]{Status: ready.Status, Message: ready.Message}, nil
	}
	if err := s.syncLocked(ctx, false); err != nil {
		return operationresult.Result[string]{}, err
	}
	invalids, err := s.invalidFilesLocked(ctx, 5)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	overview, err := s.categoryOverviewLocked(ctx)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	var out strings.Builder
	writeInvalidLongMemoryWarning(&out, invalids)
	if strings.TrimSpace(overview) == "" {
		out.WriteString("当前长期记忆库为空，尚未建立任何分类。你可以自由创建新的分类。")
		return operationresult.Result[string]{Status: operationresult.Success, Data: out.String()}, nil
	}
	out.WriteString("当前可用的长期记忆分类目录\n")
	out.WriteString(overview)
	return operationresult.Result[string]{Status: operationresult.Success, Data: strings.TrimSpace(out.String())}, nil
}

func formatSearchResult(invalids []longMemoryInvalidFile, overview string, records []longMemoryRecord, briefOnly bool) (operationresult.Result[string], error) {
	var out strings.Builder
	writeInvalidLongMemoryWarning(&out, invalids)
	if overview == "" {
		out.WriteString("全库分类概览：当前长期记忆库为空，尚未建立任何分类。\n")
	} else {
		out.WriteString("全库分类概览\n")
		out.WriteString(overview)
		out.WriteString("\n")
	}
	out.WriteString(strings.Repeat("=", 30))
	out.WriteString("\n")
	if len(records) == 0 {
		out.WriteString("长期记忆库中未找到相关内容。建议检查关键词是否过于严格，尝试 match_mode=OR，或参考分类概览调整分类。")
		return operationresult.Result[string]{Status: operationresult.Success, Data: strings.TrimSpace(out.String())}, nil
	}
	out.WriteString(fmt.Sprintf("成功检索到%d条记录\n", len(records)))
	invalidSet := map[string]bool{}
	for _, invalid := range invalids {
		invalidSet[invalid.FilePath] = true
	}
	for _, record := range records {
		out.WriteString(fmt.Sprintf("\n记忆ID：%d\n分类：%s\n标题：%s\n摘要：%s\n创建时间：%s\n更新时间：%s\n文件：%s\n", record.ID, record.Category, record.Title, record.Summary, record.CreatedAt, record.UpdatedAt, record.FilePath))
		if invalidSet[record.FilePath] {
			out.WriteString("注意：该文件当前格式损坏，以上元数据可能来自上次成功索引。\n")
		}
		if !briefOnly {
			if invalidSet[record.FilePath] {
				out.WriteString("详细内容：文件格式损坏，无法安全读取正文。请按格式警告修复。\n")
				continue
			}
			parsed, err := parseLongMemoryFile(record.FilePath)
			if err != nil {
				return operationresult.Result[string]{}, err
			}
			if parsed.Status != operationresult.Success {
				return operationresult.Result[string]{Status: parsed.Status, Message: parsed.Message}, nil
			}
			out.WriteString("详细内容（正文行号从 1 开始，可用于 content_edits）：\n")
			writeNumberedLongMemoryContent(&out, parsed.Data.Content)
		}
	}
	return operationresult.Result[string]{Status: operationresult.Success, Data: strings.TrimSpace(out.String())}, nil
}

func writeNumberedLongMemoryContent(out *strings.Builder, content string) {
	lines := fileops.SplitLines(content)
	if len(lines) == 0 {
		out.WriteString("  (空)\n")
		return
	}
	width := len(fmt.Sprintf("%d", len(lines)))
	for i, line := range lines {
		fmt.Fprintf(out, "%*d | %s\n", width, i+1, line)
	}
}

func writeInvalidLongMemoryWarning(out *strings.Builder, invalids []longMemoryInvalidFile) {
	if len(invalids) == 0 {
		return
	}
	out.WriteString("长期记忆格式警告\n以下文件无法同步到索引，请手动修复：\n")
	for _, invalid := range invalids {
		out.WriteString(fmt.Sprintf("- %s\n  原因：%s\n  建议：修复 +++ 之间的 TOML 元数据；若无法修复，可用 long_memory_write 新建记忆，然后从原文件手动复制正文。\n", invalid.FilePath, invalid.Error))
	}
	out.WriteString(strings.Repeat("=", 30))
	out.WriteString("\n")
}

func normalizeLongMemoryLimit(limit int) int {
	if limit <= 0 {
		return longMemoryDefaultLimit
	}
	if limit > longMemoryMaxLimit {
		return longMemoryMaxLimit
	}
	return limit
}

func normalizeLongMemoryMatchMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "AND") {
		return "AND"
	}
	return "OR"
}

func longMemorySearchTokens(text string) []string {
	return strings.Fields(tokenizeLongMemory(text))
}

func longMemoryFTSQuery(tokens []string, matchMode string) string {
	parts := make([]string, 0, len(tokens))
	for _, token := range tokens {
		token = strings.ReplaceAll(token, `"`, `""`)
		parts = append(parts, `"`+token+`"`)
	}
	operator := " OR "
	if matchMode == "AND" {
		operator = " AND "
	}
	return strings.Join(parts, operator)
}

func tokenizeLongMemory(text string) string {
	words := []string{}
	runes := []rune(strings.ToLower(text))
	for i := 0; i < len(runes); {
		r := runes[i]
		if isLongMemoryAlphaNum(r) {
			start := i
			for i < len(runes) && isLongMemoryAlphaNum(runes[i]) {
				i++
			}
			words = append(words, string(runes[start:i]))
			continue
		}
		if isLongMemoryCJK(r) {
			start := i
			for i < len(runes) && isLongMemoryCJK(runes[i]) {
				i++
			}
			segment := runes[start:i]
			for j := range segment {
				words = append(words, string(segment[j]))
				if j+1 < len(segment) {
					words = append(words, string(segment[j:j+2]))
				}
			}
			continue
		}
		i++
	}
	return strings.Join(words, " ")
}

func isLongMemoryAlphaNum(r rune) bool {
	return r == '_' || unicode.IsLetter(r) && r <= unicode.MaxASCII || unicode.IsDigit(r) && r <= unicode.MaxASCII
}

func isLongMemoryCJK(r rune) bool {
	return unicode.Is(unicode.Han, r)
}
