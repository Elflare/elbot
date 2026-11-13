// 解析、校验和写入长期记忆文件，维护文件名及元数据格式。
package long_memory

import (
	"context"
	"elbot/internal/fileops"
	"elbot/internal/operationresult"
	"fmt"
	"github.com/pelletier/go-toml/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type longMemoryFrontMatter struct {
	ID        int64    `toml:"id"`
	Category  string   `toml:"category"`
	Title     string   `toml:"title"`
	Summary   string   `toml:"summary"`
	Keywords  []string `toml:"keywords"`
	CreatedAt string   `toml:"created_at"`
	UpdatedAt string   `toml:"updated_at"`
}

type parsedLongMemoryFile struct {
	Meta    longMemoryFrontMatter
	Content string
}

func (s *longMemoryStore) writeRecordFileLocked(ctx context.Context, record longMemoryRecord) (operationresult.Result[struct{}], error) {
	if err := os.MkdirAll(filepath.Dir(record.FilePath), 0o755); err != nil {
		return operationresult.Result[struct{}]{}, fmt.Errorf("create long memory file dir: %w", err)
	}
	data, err := marshalLongMemoryFile(record)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	prepared, err := fileops.PrepareFileWrite(ctx, record.FilePath, string(data))
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[struct{}]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	committed, err := prepared.Data.Commit(ctx)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	return operationresult.Result[struct{}]{Status: committed.Status, Message: committed.Message}, nil
}

func parseLongMemoryFile(path string) (operationresult.Result[parsedLongMemoryFile], error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return operationresult.Result[parsedLongMemoryFile]{}, fmt.Errorf("read file: %w", err)
	}
	parsed, err := parseLongMemoryText(string(data))
	if err != nil {
		return operationresult.Result[parsedLongMemoryFile]{Status: operationresult.InvalidTarget, Message: longMemoryRepairAdvice(path, err)}, nil
	}
	return operationresult.Result[parsedLongMemoryFile]{Status: operationresult.Success, Data: parsed}, nil
}

func parseLongMemoryText(text string) (parsedLongMemoryFile, error) {
	if !strings.HasPrefix(text, "+++") {
		return parsedLongMemoryFile{}, fmt.Errorf("缺少开头 TOML front matter 分隔符 +++")
	}
	remaining := text[3:]
	if strings.HasPrefix(remaining, "\r\n") {
		remaining = remaining[2:]
	} else if strings.HasPrefix(remaining, "\n") {
		remaining = remaining[1:]
	}
	idx := strings.Index(remaining, "\n+++")
	sepLen := 4
	if idx < 0 {
		idx = strings.Index(remaining, "\r\n+++")
		sepLen = 5
	}
	if idx < 0 {
		return parsedLongMemoryFile{}, fmt.Errorf("缺少结束 TOML front matter 分隔符 +++，无法可靠区分元数据和正文")
	}
	front := remaining[:idx]
	after := remaining[idx+sepLen:]
	if strings.HasPrefix(after, "\r\n") {
		after = after[2:]
	} else if strings.HasPrefix(after, "\n") {
		after = after[1:]
	}
	var meta longMemoryFrontMatter
	if err := toml.Unmarshal([]byte(front), &meta); err != nil {
		return parsedLongMemoryFile{}, fmt.Errorf("TOML front matter 解析失败：%w", err)
	}
	return parsedLongMemoryFile{Meta: meta, Content: strings.TrimSpace(after)}, nil
}

func marshalLongMemoryFile(record longMemoryRecord) ([]byte, error) {
	meta := longMemoryFrontMatter{ID: record.ID, Category: record.Category, Title: record.Title, Summary: record.Summary, Keywords: splitLongMemoryKeywords(record.Keywords), CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
	front, err := toml.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshal long memory front matter: %w", err)
	}
	return []byte("+++\n" + strings.TrimSpace(string(front)) + "\n+++\n\n" + strings.TrimSpace(record.Content) + "\n"), nil
}

func recordFromParsedFile(path string, parsed parsedLongMemoryFile, mtimeNS, size int64) longMemoryRecord {
	return longMemoryRecord{ID: parsed.Meta.ID, FilePath: path, Category: cleanLongMemoryText(parsed.Meta.Category), Title: cleanLongMemoryText(parsed.Meta.Title), Summary: cleanLongMemoryText(parsed.Meta.Summary), Keywords: strings.Join(lmTrimStringList(parsed.Meta.Keywords), " "), Content: strings.TrimSpace(parsed.Content), CreatedAt: strings.TrimSpace(parsed.Meta.CreatedAt), UpdatedAt: strings.TrimSpace(parsed.Meta.UpdatedAt), FileMTimeNS: mtimeNS, FileSize: size}
}

func validateLongMemoryRecord(record longMemoryRecord, requireContent bool) error {
	if record.ID <= 0 {
		return fmt.Errorf("id 必须是正整数")
	}
	if record.Category == "" {
		return fmt.Errorf("category is required")
	}
	if record.Title == "" {
		return fmt.Errorf("title is required")
	}
	if record.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	if requireContent && strings.TrimSpace(record.Content) == "" {
		return fmt.Errorf("content is required")
	}
	if strings.TrimSpace(record.CreatedAt) == "" || strings.TrimSpace(record.UpdatedAt) == "" {
		return fmt.Errorf("created_at 和 updated_at 是必填字段")
	}
	return nil
}

func longMemoryRepairAdvice(path string, err error) string {
	return fmt.Sprintf("损坏文件：%s\n原因：%v\n处理建议：请修复文件开头 +++ 到结束 +++ 之间的 TOML 元数据；如果无法修复，请用 long_memory_write 新建记忆，再从原文件手动复制正文。原文件未被修改。", path, err)
}

func splitLongMemoryKeywords(text string) []string {
	text = strings.NewReplacer(",", " ", "，", " ", ";", " ", "；", " ", "\n", " ", "\r", " ", "\t", " ").Replace(text)
	return lmTrimStringList(strings.Fields(text))
}

func cleanLongMemoryText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func lmTrimStringList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

var longMemorySlugRegexp = regexp.MustCompile(`[^a-zA-Z0-9\p{Han}]+`)

func slugLongMemoryTitle(title string) string {
	slug := strings.Trim(longMemorySlugRegexp.ReplaceAllString(title, "-"), "-")
	if slug == "" {
		slug = "memory"
	}
	runes := []rune(slug)
	if len(runes) > 40 {
		slug = string(runes[:40])
	}
	return slug
}
