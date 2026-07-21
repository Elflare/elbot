package builtin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/pelletier/go-toml/v2"

	"elbot/internal/llm"
	"elbot/internal/tool"
	"elbot/internal/utils/fileops"

	_ "modernc.org/sqlite"
)

const (
	longMemoryToolName       = "long_memory"
	longMemorySearchToolName = "long_memory_search"
	longMemoryWriteToolName  = "long_memory_write"
	longMemorySyncCooldown   = 30 * time.Second
	longMemoryDefaultLimit   = 3
	longMemoryMaxLimit       = 10
)

type LongMemoryTool struct{ store *longMemoryStore }
type LongMemorySearchTool struct{ store *longMemoryStore }
type LongMemoryWriteTool struct{ store *longMemoryStore }

type longMemorySaveArgs struct {
	Category string `json:"category"`
	Title    string `json:"title"`
	Summary  string `json:"summary"`
	Keywords string `json:"keywords"`
	Content  string `json:"content"`
}

type longMemorySearchArgs struct {
	Keywords  string `json:"keywords"`
	Category  string `json:"category"`
	MatchMode string `json:"match_mode"`
	BriefOnly *bool  `json:"brief_only"`
	Limit     int    `json:"limit"`
}

type longMemoryUpdateArgs struct {
	ID           int64
	Category     *string
	Title        *string
	Summary      *string
	Keywords     *string
	Content      *string
	ContentEdits []fileops.Edit
}

type longMemoryWriteArgs struct {
	Operation    string         `json:"operation"`
	ID           int64          `json:"id"`
	Category     string         `json:"category"`
	Title        string         `json:"title"`
	Summary      string         `json:"summary"`
	Keywords     string         `json:"keywords"`
	Content      string         `json:"content"`
	ContentEdits []fileops.Edit `json:"content_edits"`
}

type longMemoryUpdatePreview struct {
	Before      longMemoryRecord
	After       longMemoryRecord
	File        fileops.File
	FullContent string
	Diff        string
	Changes     []string
}

type longMemoryRecord struct {
	ID          int64
	FilePath    string
	Category    string
	Title       string
	Summary     string
	Keywords    string
	Content     string
	CreatedAt   string
	UpdatedAt   string
	FileMTimeNS int64
	FileSize    int64
	Invalid     bool
}

type longMemoryInvalidFile struct {
	FilePath    string
	Error       string
	DetectedAt  string
	FileMTimeNS int64
	FileSize    int64
}

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

type longMemoryStore struct {
	rootDir     string
	memoriesDir string
	indexPath   string

	mu           sync.Mutex
	db           *sql.DB
	ftsAvailable bool
	initialized  bool
	lastSync     time.Time
}

func NewLongMemoryTools(rootDir string) []tool.Tool {
	store := newLongMemoryStore(rootDir)
	return []tool.Tool{
		LongMemoryTool{store: store},
		LongMemorySearchTool{store: store},
		LongMemoryWriteTool{store: store},
	}
}

func newLongMemoryStore(rootDir string) *longMemoryStore {
	rootDir = strings.TrimSpace(rootDir)
	return &longMemoryStore{rootDir: rootDir, memoriesDir: filepath.Join(rootDir, "memories"), indexPath: filepath.Join(rootDir, "index.db")}
}

func (LongMemoryTool) Name() string { return longMemoryToolName }
func (t LongMemoryTool) Info() tool.Info {
	return longMemoryBuilder(t.Name(), "Long Memory 长期记忆入口。用于检索、保存、更新、删除全局长期记忆").DependsOn(longMemorySearchToolName, longMemoryWriteToolName).BuildInfo()
}
func (t LongMemoryTool) Schema() llm.ToolSchema {
	return longMemoryBuilder(t.Name(), t.Info().Description).BuildSchema()
}
func (t LongMemoryTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	return textResult("long_memory 是长期记忆入口。请按需调用 long_memory_search 或 long_memory_write。long_memory_search 不传 keywords/category 时会返回分类目录。"), nil
}

func (LongMemorySearchTool) Name() string { return longMemorySearchToolName }
func (t LongMemorySearchTool) Info() tool.Info {
	return hiddenLongMemoryInfo(t.Name(), "检索全局长期记忆；不传 keywords/category 时返回分类目录。默认只返回标题和摘要，需要详细内容时传 brief_only=false。", tool.RiskMedium)
}
func (t LongMemorySearchTool) Schema() llm.ToolSchema {
	return longMemoryBuilder(t.Name(), t.Info().Description).
		String("keywords", "关键词。留空且 category 也为空时返回当前分类目录。").
		String("category", "指定分类。留空且 keywords 也为空时返回当前分类目录。").
		String("match_mode", "多关键词匹配模式：AND=必须全包含；OR=包含任一即可。默认 OR。").
		Boolean("brief_only", "true=仅返回标题和摘要；false=包含完整内容。默认 true。需要详细内容时再传 false。").
		Integer("limit", "最大结果数，默认 3，上限 10。").
		BuildSchema()
}
func (t LongMemorySearchTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args longMemorySearchArgs
	if err := lmDecodeArgs(req.Arguments, &args, t.Name()); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Keywords) == "" && strings.TrimSpace(args.Category) == "" {
		content, err := t.store.categories(ctx)
		if err != nil {
			return nil, err
		}
		return textResult(content), nil
	}
	content, err := t.store.search(ctx, args)
	if err != nil {
		return nil, err
	}
	return textResult(content), nil
}

func (LongMemoryWriteTool) Name() string { return longMemoryWriteToolName }
func (t LongMemoryWriteTool) Info() tool.Info {
	return hiddenLongMemoryInfo(t.Name(), "保存、局部更新或永久删除全局长期记忆。operation 为 save、update 或 delete。保存前建议先用 long_memory_search 查看分类和避免重复。", tool.RiskHigh)
}
func (t LongMemoryWriteTool) Schema() llm.ToolSchema {
	return longMemoryBuilder(t.Name(), t.Info().Description).
		String("operation", "写操作：save、update、delete。", tool.Required()).
		Integer("id", "update/delete 需要的记忆 ID。").
		String("category", "save 需要；update 时不填表示保持不变，填写表示修改分类。优先复用已有分类，避免创建语义重叠的新分类。").
		String("title", "save 需要；update 时不填表示保持不变，填写表示修改标题。").
		String("summary", "save 需要；update 时不填表示保持不变，填写表示修改摘要，建议 50 字以内。").
		String("keywords", "save 需要；update 时不填表示保持不变，填写表示修改搜索关键词；用空格、逗号或换行分隔。").
		String("content", "save 需要；update 时不填表示保持不变，填写表示整段替换正文；delete 会忽略。update 时不能和 content_edits 同时填写。").
		ObjectArray("content_edits", "仅 update 有效；不能和 content 同时填写。使用与 edit_file 相同的精确文本、anchor、行号插入/删除协议。", fileops.EditOperationProperties(), []string{"operation"}).
		BuildSchema()
}

func (t LongMemoryWriteTool) PreflightConfirmation(ctx context.Context, req tool.CallRequest) error {
	args, err := t.decodeWriteArgs(req)
	if err != nil {
		return err
	}
	if strings.ToLower(strings.TrimSpace(args.Operation)) != "update" {
		return nil
	}
	_, err = t.store.previewUpdate(ctx, longMemoryUpdateArgsFromWrite(args))
	return err
}

func (t LongMemoryWriteTool) RiskDetail(ctx context.Context, req tool.CallRequest) (string, error) {
	args, err := t.decodeWriteArgs(req)
	if err != nil {
		return "", err
	}
	if strings.ToLower(strings.TrimSpace(args.Operation)) != "update" {
		return "", nil
	}
	preview, err := t.store.previewUpdate(ctx, longMemoryUpdateArgsFromWrite(args))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "操作：更新长期记忆\n")
	fmt.Fprintf(&b, "ID：%d\n", preview.After.ID)
	fmt.Fprintf(&b, "文件：%s\n", preview.After.FilePath)
	fmt.Fprintf(&b, "分类：%s\n", preview.After.Category)
	fmt.Fprintf(&b, "标题：%s\n", preview.After.Title)
	b.WriteString("模式：确认后写入；确认前已自动预检\n")
	if len(args.ContentEdits) > 0 {
		b.WriteString("正文编辑：content_edits 只作用于正文；所有目标均基于更新前正文解析\n")
	}
	if len(preview.Changes) > 0 {
		b.WriteString("字段变化：\n")
		for _, change := range preview.Changes {
			fmt.Fprintf(&b, "- %s\n", change)
		}
	}
	b.WriteString("预检 diff:\n")
	b.WriteString(preview.Diff)
	if !strings.HasSuffix(preview.Diff, "\n") {
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (t LongMemoryWriteTool) decodeWriteArgs(req tool.CallRequest) (longMemoryWriteArgs, error) {
	var args longMemoryWriteArgs
	if err := lmDecodeArgs(req.Arguments, &args, t.Name()); err != nil {
		return longMemoryWriteArgs{}, err
	}
	return args, nil
}

func longMemoryUpdateArgsFromWrite(args longMemoryWriteArgs) longMemoryUpdateArgs {
	update := longMemoryUpdateArgs{ID: args.ID, ContentEdits: args.ContentEdits}
	if args.Category != "" {
		update.Category = &args.Category
	}
	if args.Title != "" {
		update.Title = &args.Title
	}
	if args.Summary != "" {
		update.Summary = &args.Summary
	}
	if args.Keywords != "" {
		update.Keywords = &args.Keywords
	}
	if args.Content != "" {
		update.Content = &args.Content
	}
	return update
}

func (t LongMemoryWriteTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	args, err := t.decodeWriteArgs(req)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(args.Operation)) {
	case "save":
		record, err := t.store.save(ctx, longMemorySaveArgs{Category: args.Category, Title: args.Title, Summary: args.Summary, Keywords: args.Keywords, Content: args.Content})
		if err != nil {
			return nil, err
		}
		return textResult(fmt.Sprintf("记忆写入成功！\nID：%d\n分类：%s\n标题：%s\n文件：%s\n可通过 long_memory_search 检索，或用 long_memory_write 更新/删除。", record.ID, record.Category, record.Title, record.FilePath)), nil
	case "update":
		record, err := t.store.update(ctx, longMemoryUpdateArgsFromWrite(args))
		if err != nil {
			return nil, err
		}
		return textResult(fmt.Sprintf("ID：%d 更新成功！\n分类：%s\n标题：%s\n文件：%s", record.ID, record.Category, record.Title, record.FilePath)), nil
	case "delete":
		path, err := t.store.delete(ctx, args.ID)
		if err != nil {
			return nil, err
		}
		return textResult(fmt.Sprintf("记忆ID：%d已永久删除。\n文件：%s", args.ID, path)), nil
	default:
		return nil, fmt.Errorf("operation must be save, update, or delete")
	}
}

func longMemoryBuilder(name, description string) *tool.Builder {
	return tool.NewBuilder(name).Description(description).Risk(tool.RiskMedium).Tags("agent").SuperadminOnly()
}

func hiddenLongMemoryInfo(name, description string, risk tool.RiskLevel) tool.Info {
	return longMemoryBuilder(name, description).Risk(risk).Hidden().SuperadminOnly().BuildInfo()
}

func (s *longMemoryStore) save(ctx context.Context, args longMemorySaveArgs) (longMemoryRecord, error) {
	if err := ctx.Err(); err != nil {
		return longMemoryRecord{}, err
	}
	record := longMemoryRecord{Category: cleanLongMemoryText(args.Category), Title: cleanLongMemoryText(args.Title), Summary: cleanLongMemoryText(args.Summary), Keywords: strings.Join(splitLongMemoryKeywords(args.Keywords), " "), Content: strings.TrimSpace(args.Content)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLocked(ctx); err != nil {
		return longMemoryRecord{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	record.ID = s.nextIDLocked(ctx)
	record.CreatedAt = now
	record.UpdatedAt = now
	record.FilePath = filepath.Join(s.memoriesDir, fmt.Sprintf("%06d-%s.md", record.ID, slugLongMemoryTitle(record.Title)))
	if err := validateLongMemoryRecord(record, true); err != nil {
		return longMemoryRecord{}, err
	}
	if err := s.writeRecordFileLocked(record); err != nil {
		return longMemoryRecord{}, err
	}
	if err := s.indexRecordLocked(ctx, record); err != nil {
		return longMemoryRecord{}, err
	}
	return record, nil
}

func (s *longMemoryStore) search(ctx context.Context, args longMemorySearchArgs) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	keywords := strings.TrimSpace(args.Keywords)
	category := cleanLongMemoryText(args.Category)
	if keywords == "" && category == "" {
		return "", fmt.Errorf("keywords 和 category 不能同时为空")
	}
	briefOnly := true
	if args.BriefOnly != nil {
		briefOnly = *args.BriefOnly
	}
	limit := normalizeLongMemoryLimit(args.Limit)
	matchMode := normalizeLongMemoryMatchMode(args.MatchMode)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLocked(ctx); err != nil {
		return "", err
	}
	if err := s.syncLocked(ctx, false); err != nil {
		return "", err
	}
	invalids, _ := s.invalidFilesLocked(ctx, 5)
	overview, err := s.categoryOverviewLocked(ctx)
	if err != nil {
		return "", err
	}
	records, err := s.searchFTSLocked(ctx, keywords, category, matchMode, limit)
	if err != nil || !s.ftsAvailable {
		records, err = s.searchLikeLocked(ctx, keywords, category, matchMode, limit)
	}
	if err != nil {
		return "", err
	}
	return s.formatSearchResult(ctx, invalids, overview, records, briefOnly), nil
}

func (s *longMemoryStore) categories(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLocked(ctx); err != nil {
		return "", err
	}
	if err := s.syncLocked(ctx, false); err != nil {
		return "", err
	}
	invalids, _ := s.invalidFilesLocked(ctx, 5)
	overview, err := s.categoryOverviewLocked(ctx)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	writeInvalidLongMemoryWarning(&out, invalids)
	if strings.TrimSpace(overview) == "" {
		out.WriteString("当前长期记忆库为空，尚未建立任何分类。你可以自由创建新的分类。")
		return out.String(), nil
	}
	out.WriteString("当前可用的长期记忆分类目录\n")
	out.WriteString(overview)
	return strings.TrimSpace(out.String()), nil
}

func (s *longMemoryStore) update(ctx context.Context, args longMemoryUpdateArgs) (longMemoryRecord, error) {
	if err := ctx.Err(); err != nil {
		return longMemoryRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	preview, err := s.previewUpdateLocked(ctx, args)
	if err != nil {
		return longMemoryRecord{}, err
	}
	if err := s.writeUpdatePreviewLocked(ctx, preview); err != nil {
		return longMemoryRecord{}, err
	}
	if err := s.indexRecordLocked(ctx, preview.After); err != nil {
		return longMemoryRecord{}, err
	}
	return preview.After, nil
}

func (s *longMemoryStore) previewUpdate(ctx context.Context, args longMemoryUpdateArgs) (longMemoryUpdatePreview, error) {
	if err := ctx.Err(); err != nil {
		return longMemoryUpdatePreview{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.previewUpdateLocked(ctx, args)
}

func (s *longMemoryStore) previewUpdateLocked(ctx context.Context, args longMemoryUpdateArgs) (longMemoryUpdatePreview, error) {
	if args.ID <= 0 {
		return longMemoryUpdatePreview{}, fmt.Errorf("id is required")
	}
	if args.Content != nil && len(args.ContentEdits) > 0 {
		return longMemoryUpdatePreview{}, fmt.Errorf("content and content_edits cannot be used together")
	}
	if !args.hasChanges() {
		return longMemoryUpdatePreview{}, fmt.Errorf("update requires at least one changed field or content_edits")
	}
	if err := s.ensureLocked(ctx); err != nil {
		return longMemoryUpdatePreview{}, err
	}
	record, err := s.recordByIDLocked(ctx, args.ID)
	if errors.Is(err, sql.ErrNoRows) {
		if syncErr := s.syncLocked(ctx, true); syncErr != nil {
			return longMemoryUpdatePreview{}, syncErr
		}
		record, err = s.recordByIDLocked(ctx, args.ID)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return longMemoryUpdatePreview{}, fmt.Errorf("未找到 ID 为 %d 的长期记忆", args.ID)
		}
		return longMemoryUpdatePreview{}, err
	}
	file, err := fileops.ReadFile(record.FilePath, "")
	if err != nil {
		return longMemoryUpdatePreview{}, err
	}
	parsed, err := parseLongMemoryText(file.Text)
	if err != nil {
		return longMemoryUpdatePreview{}, fmt.Errorf("无法更新：记忆文件格式损坏，工具不会覆盖原文件。\n%s", longMemoryRepairAdvice(record.FilePath, err))
	}
	record = recordFromParsedFile(record.FilePath, parsed, record.FileMTimeNS, record.FileSize)
	next := record
	changes := []string{}
	if args.Category != nil {
		old := next.Category
		next.Category = cleanLongMemoryText(*args.Category)
		changes = appendLongMemoryChange(changes, "分类", old, next.Category)
	}
	if args.Title != nil {
		old := next.Title
		next.Title = cleanLongMemoryText(*args.Title)
		changes = appendLongMemoryChange(changes, "标题", old, next.Title)
	}
	if args.Summary != nil {
		old := next.Summary
		next.Summary = cleanLongMemoryText(*args.Summary)
		changes = appendLongMemoryChange(changes, "摘要", old, next.Summary)
	}
	if args.Keywords != nil {
		old := next.Keywords
		next.Keywords = strings.Join(splitLongMemoryKeywords(*args.Keywords), " ")
		changes = appendLongMemoryChange(changes, "关键词", old, next.Keywords)
	}
	if args.Content != nil {
		old := next.Content
		next.Content = strings.TrimSpace(*args.Content)
		changes = appendLongMemoryChange(changes, "正文", old, next.Content)
	}
	if len(args.ContentEdits) > 0 {
		content, err := fileops.ApplyEdits(next.Content, args.ContentEdits)
		if err != nil {
			return longMemoryUpdatePreview{}, err
		}
		old := next.Content
		next.Content = strings.TrimSpace(content)
		changes = appendLongMemoryChange(changes, "正文", old, next.Content)
	}
	if err := validateLongMemoryRecord(next, true); err != nil {
		return longMemoryUpdatePreview{}, err
	}
	if len(changes) == 0 {
		return longMemoryUpdatePreview{}, fmt.Errorf("edit produced no changes")
	}
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if next.CreatedAt == "" {
		next.CreatedAt = next.UpdatedAt
	}
	data, err := marshalLongMemoryFile(next)
	if err != nil {
		return longMemoryUpdatePreview{}, err
	}
	oldText := fileops.NormalizeEditText(file.Text)
	newText := fileops.NormalizeEditText(string(data))
	if oldText == newText {
		return longMemoryUpdatePreview{}, fmt.Errorf("edit produced no changes")
	}
	return longMemoryUpdatePreview{Before: record, After: next, File: file, FullContent: string(data), Diff: fileops.UnifiedDiff(record.FilePath, fileops.SplitLines(oldText), fileops.SplitLines(newText), 3), Changes: changes}, nil
}

func (args longMemoryUpdateArgs) hasChanges() bool {
	return args.Category != nil || args.Title != nil || args.Summary != nil || args.Keywords != nil || args.Content != nil || len(args.ContentEdits) > 0
}

func appendLongMemoryChange(changes []string, label, oldValue, newValue string) []string {
	if oldValue == newValue {
		return changes
	}
	return append(changes, fmt.Sprintf("%s：%s -> %s", label, longMemoryInlineValue(oldValue), longMemoryInlineValue(newValue)))
}

func longMemoryInlineValue(value string) string {
	value = cleanLongMemoryText(value)
	if value == "" {
		return "(空)"
	}
	return value
}

func (s *longMemoryStore) writeUpdatePreviewLocked(ctx context.Context, preview longMemoryUpdatePreview) error {
	edit := fileops.Edit{Operation: "overwrite", NewText: &preview.FullContent}
	_, err := fileops.EditFile(preview.After.FilePath, preview.File.Encoding, fileops.ContentRevision(preview.File.Bytes), false, false, 3, []fileops.Edit{edit})
	if err != nil {
		return err
	}
	return ctx.Err()
}

func (s *longMemoryStore) delete(ctx context.Context, id int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if id <= 0 {
		return "", fmt.Errorf("id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLocked(ctx); err != nil {
		return "", err
	}
	record, err := s.recordByIDLocked(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		if syncErr := s.syncLocked(ctx, true); syncErr != nil {
			return "", syncErr
		}
		record, err = s.recordByIDLocked(ctx, id)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("未找到 ID 为 %d 的长期记忆", id)
		}
		return "", err
	}
	if err := os.Remove(record.FilePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("delete memory file %q: %w", record.FilePath, err)
	}
	if err := s.deleteIndexLocked(ctx, id, record.FilePath); err != nil {
		return "", err
	}
	return record.FilePath, nil
}

func (s *longMemoryStore) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	s.initialized = false
	s.ftsAvailable = false
	return err
}

func (s *longMemoryStore) ensureLocked(ctx context.Context) error {
	if s.initialized {
		return nil
	}
	if strings.TrimSpace(s.rootDir) == "" {
		return fmt.Errorf("long memory directory is not configured")
	}
	if err := os.MkdirAll(s.memoriesDir, 0o755); err != nil {
		return fmt.Errorf("create long memory directory %q: %w", s.memoriesDir, err)
	}
	db, err := sql.Open("sqlite", s.indexPath)
	if err != nil {
		return fmt.Errorf("open long memory index: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return fmt.Errorf("enable long memory foreign keys: %w", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS memory_files (
		id INTEGER PRIMARY KEY,
		file_path TEXT NOT NULL UNIQUE,
		category TEXT NOT NULL,
		title TEXT NOT NULL,
		summary TEXT NOT NULL,
		keywords TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		file_mtime_ns INTEGER NOT NULL,
		file_size INTEGER NOT NULL
	)`); err != nil {
		_ = db.Close()
		return fmt.Errorf("create long memory metadata table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS memory_invalid_files (
		file_path TEXT PRIMARY KEY,
		error TEXT NOT NULL,
		detected_at TEXT NOT NULL,
		file_mtime_ns INTEGER NOT NULL,
		file_size INTEGER NOT NULL
	)`); err != nil {
		_ = db.Close()
		return fmt.Errorf("create long memory invalid table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(category, title, summary, keywords, content_tokens, content_id UNINDEXED)`); err == nil {
		s.ftsAvailable = true
	} else {
		s.ftsAvailable = false
	}
	s.db = db
	s.initialized = true
	return nil
}

func (s *longMemoryStore) syncLocked(ctx context.Context, force bool) error {
	if !force && !s.lastSync.IsZero() && time.Since(s.lastSync) < longMemorySyncCooldown {
		return nil
	}
	s.lastSync = time.Now()
	entries, err := os.ReadDir(s.memoriesDir)
	if err != nil {
		return fmt.Errorf("read long memory directory %q: %w", s.memoriesDir, err)
	}
	seen := map[string]bool{}
	seenIDs := map[int64]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		path := filepath.Join(s.memoriesDir, entry.Name())
		seen[path] = true
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat long memory file %q: %w", path, err)
		}
		mtimeNS := info.ModTime().UnixNano()
		size := info.Size()
		indexed, err := s.fileStateByPathLocked(ctx, path)
		if err == nil && indexed.FileMTimeNS == mtimeNS && indexed.FileSize == size {
			continue
		}
		parsed, err := parseLongMemoryFile(path)
		if err != nil {
			if writeErr := s.upsertInvalidLocked(ctx, path, err.Error(), mtimeNS, size); writeErr != nil {
				return writeErr
			}
			continue
		}
		record := recordFromParsedFile(path, parsed, mtimeNS, size)
		if err := validateLongMemoryRecord(record, false); err != nil {
			if writeErr := s.upsertInvalidLocked(ctx, path, err.Error(), mtimeNS, size); writeErr != nil {
				return writeErr
			}
			continue
		}
		if previous, ok := seenIDs[record.ID]; ok && previous != path {
			if writeErr := s.upsertInvalidLocked(ctx, path, fmt.Sprintf("id %d 与文件 %s 重复", record.ID, previous), mtimeNS, size); writeErr != nil {
				return writeErr
			}
			continue
		}
		seenIDs[record.ID] = path
		if err := s.indexRecordLocked(ctx, record); err != nil {
			return err
		}
	}
	paths, err := s.indexedPathsLocked(ctx)
	if err != nil {
		return err
	}
	for _, indexed := range paths {
		if !seen[indexed.FilePath] {
			if err := s.deleteIndexLocked(ctx, indexed.ID, indexed.FilePath); err != nil {
				return err
			}
		}
	}
	invalidPaths, err := s.invalidPathsLocked(ctx)
	if err != nil {
		return err
	}
	for _, path := range invalidPaths {
		if !seen[path] {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM memory_invalid_files WHERE file_path = ?`, path); err != nil {
				return fmt.Errorf("delete stale invalid long memory %q: %w", path, err)
			}
		}
	}
	return nil
}

func (s *longMemoryStore) writeRecordFileLocked(record longMemoryRecord) error {
	if err := os.MkdirAll(filepath.Dir(record.FilePath), 0o755); err != nil {
		return fmt.Errorf("create long memory file dir: %w", err)
	}
	data, err := marshalLongMemoryFile(record)
	if err != nil {
		return err
	}
	tmp := record.FilePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp long memory file %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, record.FilePath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace long memory file %q: %w", record.FilePath, err)
	}
	return nil
}

func (s *longMemoryStore) indexRecordLocked(ctx context.Context, record longMemoryRecord) error {
	info, err := os.Stat(record.FilePath)
	if err != nil {
		return fmt.Errorf("stat long memory file %q: %w", record.FilePath, err)
	}
	record.FileMTimeNS = info.ModTime().UnixNano()
	record.FileSize = info.Size()
	_, err = s.db.ExecContext(ctx, `INSERT INTO memory_files (id, file_path, category, title, summary, keywords, created_at, updated_at, file_mtime_ns, file_size)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET file_path=excluded.file_path, category=excluded.category, title=excluded.title, summary=excluded.summary, keywords=excluded.keywords, created_at=excluded.created_at, updated_at=excluded.updated_at, file_mtime_ns=excluded.file_mtime_ns, file_size=excluded.file_size`,
		record.ID, record.FilePath, record.Category, record.Title, record.Summary, record.Keywords, record.CreatedAt, record.UpdatedAt, record.FileMTimeNS, record.FileSize)
	if err != nil {
		return fmt.Errorf("index long memory metadata: %w", err)
	}
	if s.ftsAvailable {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM memory_fts WHERE content_id = ?`, record.ID)
		_, err = s.db.ExecContext(ctx, `INSERT INTO memory_fts (category, title, summary, keywords, content_tokens, content_id) VALUES (?, ?, ?, ?, ?, ?)`, tokenizeLongMemory(record.Category), tokenizeLongMemory(record.Title), tokenizeLongMemory(record.Summary), strings.Join(splitLongMemoryKeywords(record.Keywords), " "), tokenizeLongMemory(record.Content), record.ID)
		if err != nil {
			s.ftsAvailable = false
		}
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM memory_invalid_files WHERE file_path = ?`, record.FilePath)
	if err != nil {
		return fmt.Errorf("clear long memory invalid state: %w", err)
	}
	return nil
}

func (s *longMemoryStore) deleteIndexLocked(ctx context.Context, id int64, path string) error {
	if s.ftsAvailable {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM memory_fts WHERE content_id = ?`, id)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM memory_files WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete long memory metadata: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM memory_invalid_files WHERE file_path = ?`, path); err != nil {
		return fmt.Errorf("delete long memory invalid state: %w", err)
	}
	return nil
}

func (s *longMemoryStore) fileStateByPathLocked(ctx context.Context, path string) (longMemoryRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, file_path, file_mtime_ns, file_size FROM memory_files WHERE file_path = ?`, path)
	var record longMemoryRecord
	err := row.Scan(&record.ID, &record.FilePath, &record.FileMTimeNS, &record.FileSize)
	return record, err
}

func (s *longMemoryStore) recordByIDLocked(ctx context.Context, id int64) (longMemoryRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, file_path, category, title, summary, keywords, created_at, updated_at, file_mtime_ns, file_size FROM memory_files WHERE id = ?`, id)
	var record longMemoryRecord
	err := row.Scan(&record.ID, &record.FilePath, &record.Category, &record.Title, &record.Summary, &record.Keywords, &record.CreatedAt, &record.UpdatedAt, &record.FileMTimeNS, &record.FileSize)
	return record, err
}

func (s *longMemoryStore) indexedPathsLocked(ctx context.Context) ([]longMemoryRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, file_path FROM memory_files`)
	if err != nil {
		return nil, fmt.Errorf("list indexed long memory paths: %w", err)
	}
	defer rows.Close()
	var records []longMemoryRecord
	for rows.Next() {
		var record longMemoryRecord
		if err := rows.Scan(&record.ID, &record.FilePath); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *longMemoryStore) invalidPathsLocked(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT file_path FROM memory_invalid_files`)
	if err != nil {
		return nil, fmt.Errorf("list invalid long memory paths: %w", err)
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}

func (s *longMemoryStore) upsertInvalidLocked(ctx context.Context, path, reason string, mtimeNS, size int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO memory_invalid_files (file_path, error, detected_at, file_mtime_ns, file_size)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(file_path) DO UPDATE SET error=excluded.error, detected_at=excluded.detected_at, file_mtime_ns=excluded.file_mtime_ns, file_size=excluded.file_size`, path, reason, time.Now().UTC().Format(time.RFC3339), mtimeNS, size)
	if err != nil {
		return fmt.Errorf("record invalid long memory %q: %w", path, err)
	}
	return nil
}

func (s *longMemoryStore) invalidFilesLocked(ctx context.Context, limit int) ([]longMemoryInvalidFile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT file_path, error, detected_at, file_mtime_ns, file_size FROM memory_invalid_files ORDER BY detected_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list invalid long memories: %w", err)
	}
	defer rows.Close()
	var files []longMemoryInvalidFile
	for rows.Next() {
		var file longMemoryInvalidFile
		if err := rows.Scan(&file.FilePath, &file.Error, &file.DetectedAt, &file.FileMTimeNS, &file.FileSize); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func (s *longMemoryStore) categoryOverviewLocked(ctx context.Context) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT category, COUNT(*) FROM memory_files GROUP BY category ORDER BY COUNT(*) DESC, category ASC`)
	if err != nil {
		return "", fmt.Errorf("query long memory categories: %w", err)
	}
	defer rows.Close()
	var out strings.Builder
	for rows.Next() {
		var category string
		var count int
		if err := rows.Scan(&category, &count); err != nil {
			return "", err
		}
		out.WriteString(fmt.Sprintf("-%s（共%d条记录）\n", category, count))
	}
	return strings.TrimSpace(out.String()), rows.Err()
}

func (s *longMemoryStore) searchFTSLocked(ctx context.Context, keywords, category, matchMode string, limit int) ([]longMemoryRecord, error) {
	if !s.ftsAvailable {
		return nil, fmt.Errorf("long memory FTS is unavailable")
	}
	tokens := longMemorySearchTokens(keywords)
	sqlText := `SELECT f.id, f.file_path, f.category, f.title, f.summary, f.keywords, f.created_at, f.updated_at, f.file_mtime_ns, f.file_size FROM memory_files f JOIN memory_fts x ON x.content_id = f.id WHERE 1=1`
	params := []any{}
	if len(tokens) > 0 {
		sqlText += ` AND memory_fts MATCH ?`
		params = append(params, longMemoryFTSQuery(tokens, matchMode))
	}
	if category != "" {
		sqlText += ` AND f.category = ?`
		params = append(params, category)
	}
	if len(tokens) > 0 {
		sqlText += ` ORDER BY rank, f.updated_at DESC LIMIT ?`
	} else {
		sqlText += ` ORDER BY f.updated_at DESC LIMIT ?`
	}
	params = append(params, limit)
	rows, err := s.db.QueryContext(ctx, sqlText, params...)
	if err != nil {
		s.ftsAvailable = false
		return nil, err
	}
	defer rows.Close()
	return scanLongMemoryRecords(rows)
}

func (s *longMemoryStore) searchLikeLocked(ctx context.Context, keywords, category, matchMode string, limit int) ([]longMemoryRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, file_path, category, title, summary, keywords, created_at, updated_at, file_mtime_ns, file_size FROM memory_files WHERE (? = '' OR category = ?) ORDER BY updated_at DESC`, category, category)
	if err != nil {
		return nil, fmt.Errorf("query long memory fallback: %w", err)
	}
	defer rows.Close()
	candidates, err := scanLongMemoryRecords(rows)
	if err != nil {
		return nil, err
	}
	tokens := longMemorySearchTokens(keywords)
	var matched []longMemoryRecord
	for _, record := range candidates {
		if len(tokens) == 0 {
			matched = append(matched, record)
		} else {
			content := record.Category + " " + record.Title + " " + record.Summary + " " + record.Keywords
			if parsed, err := parseLongMemoryFile(record.FilePath); err == nil {
				content += " " + parsed.Content
			}
			if longMemoryTextMatches(content, tokens, matchMode) {
				matched = append(matched, record)
			}
		}
		if len(matched) >= limit {
			break
		}
	}
	return matched, nil
}

func (s *longMemoryStore) formatSearchResult(ctx context.Context, invalids []longMemoryInvalidFile, overview string, records []longMemoryRecord, briefOnly bool) string {
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
		return strings.TrimSpace(out.String())
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
			if parsed, err := parseLongMemoryFile(record.FilePath); err == nil {
				out.WriteString("详细内容（正文行号从 1 开始，可用于 content_edits）：\n")
				writeNumberedLongMemoryContent(&out, parsed.Content)
			} else {
				out.WriteString("详细内容：文件格式损坏，无法安全读取正文。请按格式警告修复。\n")
			}
		}
	}
	return strings.TrimSpace(out.String())
}

func (s *longMemoryStore) nextIDLocked(ctx context.Context) int64 {
	var maxID sql.NullInt64
	_ = s.db.QueryRowContext(ctx, `SELECT MAX(id) FROM memory_files`).Scan(&maxID)
	id := maxID.Int64 + 1
	for {
		pathGlob := filepath.Join(s.memoriesDir, fmt.Sprintf("%06d-*.md", id))
		matches, _ := filepath.Glob(pathGlob)
		if len(matches) == 0 {
			return id
		}
		id++
	}
}

func parseLongMemoryFile(path string) (parsedLongMemoryFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return parsedLongMemoryFile{}, fmt.Errorf("read file: %w", err)
	}
	return parseLongMemoryText(string(data))
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

func scanLongMemoryRecords(rows *sql.Rows) ([]longMemoryRecord, error) {
	var records []longMemoryRecord
	for rows.Next() {
		var record longMemoryRecord
		if err := rows.Scan(&record.ID, &record.FilePath, &record.Category, &record.Title, &record.Summary, &record.Keywords, &record.CreatedAt, &record.UpdatedAt, &record.FileMTimeNS, &record.FileSize); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
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

func longMemoryRepairAdvice(path string, err error) string {
	return fmt.Sprintf("损坏文件：%s\n原因：%v\n处理建议：请修复文件开头 +++ 到结束 +++ 之间的 TOML 元数据；如果无法修复，请用 long_memory_write 新建记忆，再从原文件手动复制正文。原文件未被修改。", path, err)
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

func longMemoryTextMatches(text string, tokens []string, matchMode string) bool {
	text = strings.ToLower(text)
	if matchMode == "AND" {
		for _, token := range tokens {
			if !strings.Contains(text, strings.ToLower(token)) {
				return false
			}
		}
		return true
	}
	for _, token := range tokens {
		if strings.Contains(text, strings.ToLower(token)) {
			return true
		}
	}
	return len(tokens) == 0
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

func lmDecodeArgs(raw []byte, out any, name string) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("parse %s arguments: %w", name, err)
	}
	return nil
}
