// 保存长期记忆存储状态，处理保存、更新及删除。
package long_memory

import (
	"context"
	"database/sql"
	"elbot/internal/fileops"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type longMemoryUpdateArgs struct {
	ID           int64
	Category     *string
	Title        *string
	Summary      *string
	Keywords     *string
	Content      *string
	ContentEdits []fileops.Edit
}

type longMemoryUpdatePreview struct {
	Before  longMemoryRecord
	After   longMemoryRecord
	File    *fileops.PreparedFile // 公共文件层保存批准内容与原修订，提交时统一复核。
	Changes []string
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
	FileMTimeNS int64 // 索引记录的文件修改时间，单位为纳秒。
	FileSize    int64 // 索引记录的文件字节数。
}

type longMemoryStore struct {
	rootDir     string // 长期记忆根目录。
	memoriesDir string // 记忆 Markdown 文件目录。
	indexPath   string // SQLite 索引文件路径。

	mu          sync.Mutex // 保护数据库访问、同步状态及文件变更。
	db          *sql.DB    // 当前打开的索引数据库。
	initialized bool       // 索引表是否已初始化。
	lastSync    time.Time  // 最近成功同步时间，用于同步冷却。
	// formatErrors remembers file-local validation failures for this open store.
	// Reuse requires matching persisted error and file attributes; mu protects it.
	formatErrors map[string]longMemoryInvalidFile
}

func newLongMemoryStore(rootDir string) *longMemoryStore {
	rootDir = strings.TrimSpace(rootDir)
	return &longMemoryStore{rootDir: rootDir, memoriesDir: filepath.Join(rootDir, "memories"), indexPath: filepath.Join(rootDir, "index.db")}
}

func (s *longMemoryStore) save(ctx context.Context, record longMemoryRecord) (longMemoryRecord, error) {
	if err := ctx.Err(); err != nil {
		return longMemoryRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLocked(ctx); err != nil {
		return longMemoryRecord{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id, err := s.nextIDLocked(ctx)
	if err != nil {
		return longMemoryRecord{}, err
	}
	record.ID = id
	record.CreatedAt = now
	record.UpdatedAt = now
	record.FilePath = filepath.Join(s.memoriesDir, fmt.Sprintf("%06d-%s.md", record.ID, slugLongMemoryTitle(record.Title)))
	if err := validateLongMemoryRecord(record, true); err != nil {
		return longMemoryRecord{}, err
	}
	if err := s.writeRecordFileLocked(ctx, record); err != nil {
		return longMemoryRecord{}, err
	}
	info, err := os.Stat(record.FilePath)
	if err != nil {
		return longMemoryRecord{}, fmt.Errorf("stat long memory file %q: %w", record.FilePath, err)
	}
	record.FileMTimeNS, record.FileSize = info.ModTime().UnixNano(), info.Size()
	if err := s.indexRecordLocked(ctx, record); err != nil {
		return longMemoryRecord{}, err
	}
	return record, nil
}

func (s *longMemoryStore) update(ctx context.Context, preview longMemoryUpdatePreview) (longMemoryRecord, error) {
	if err := ctx.Err(); err != nil {
		return longMemoryRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.recordByIDLocked(ctx, preview.Before.ID)
	if err != nil {
		return longMemoryRecord{}, err
	}
	if record.FilePath != preview.Before.FilePath {
		return longMemoryRecord{}, fmt.Errorf("memory target changed after preflight")
	}
	if _, err := preview.File.Commit(ctx); err != nil {
		return longMemoryRecord{}, err
	}
	info, err := os.Stat(preview.After.FilePath)
	if err != nil {
		return longMemoryRecord{}, fmt.Errorf("stat long memory file %q: %w", preview.After.FilePath, err)
	}
	preview.After.FileMTimeNS, preview.After.FileSize = info.ModTime().UnixNano(), info.Size()
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
	var preview longMemoryUpdatePreview
	file, err := fileops.PrepareFile(ctx, record.FilePath, func(text string) (string, error) {
		parsed, err := parseLongMemoryText(text)
		if err != nil {
			return "", fmt.Errorf("无法更新：记忆文件格式损坏，工具不会覆盖原文件。\n%s", longMemoryRepairAdvice(record.FilePath, err))
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
				return "", err
			}
			old := next.Content
			next.Content = strings.TrimSpace(content)
			changes = appendLongMemoryChange(changes, "正文", old, next.Content)
		}
		if err := validateLongMemoryRecord(next, true); err != nil {
			return "", err
		}
		if len(changes) == 0 {
			return "", fmt.Errorf("edit produced no changes")
		}
		next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		data, err := marshalLongMemoryFile(next)
		if err != nil {
			return "", err
		}
		preview.Before, preview.After, preview.Changes = record, next, changes
		return string(data), nil
	})
	if err != nil {
		return longMemoryUpdatePreview{}, err
	}
	preview.File = file
	return preview, nil
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

func (s *longMemoryStore) delete(ctx context.Context, approved longMemoryRecord, target, revision string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.recordByIDLocked(ctx, approved.ID)
	if err != nil {
		return "", err
	}
	if record.FilePath != approved.FilePath {
		return "", fmt.Errorf("memory target changed after preparation")
	}
	currentTarget, err := filepath.EvalSymlinks(record.FilePath)
	if err != nil {
		return "", err
	}
	if currentTarget != target {
		return "", fmt.Errorf("memory target changed after preparation")
	}
	data, err := os.ReadFile(record.FilePath)
	if err != nil {
		return "", err
	}
	if fileops.ContentRevision(data) != revision {
		return "", fmt.Errorf("memory changed after preparation")
	}
	if err := os.Remove(record.FilePath); err != nil {
		return "", fmt.Errorf("delete memory file %q: %w", record.FilePath, err)
	}
	if err := s.deleteIndexLocked(ctx, record.ID, record.FilePath); err != nil {
		return "", err
	}
	return record.FilePath, nil
}
