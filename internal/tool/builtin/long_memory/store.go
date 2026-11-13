// 保存长期记忆存储状态，处理保存、更新及删除。
package long_memory

import (
	"context"
	"database/sql"
	"elbot/internal/fileops"
	"elbot/internal/operationresult"
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

func (s *longMemoryStore) save(ctx context.Context, record longMemoryRecord) (operationresult.Result[longMemoryRecord], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[longMemoryRecord]{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ready, err := s.ensureLocked(ctx)
	if err != nil {
		return operationresult.Result[longMemoryRecord]{}, err
	}
	if ready.Status != operationresult.Success {
		return operationresult.Result[longMemoryRecord]{Status: ready.Status, Message: ready.Message}, nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id, err := s.nextIDLocked(ctx)
	if err != nil {
		return operationresult.Result[longMemoryRecord]{}, err
	}
	record.ID = id
	record.CreatedAt = now
	record.UpdatedAt = now
	record.FilePath = filepath.Join(s.memoriesDir, fmt.Sprintf("%06d-%s.md", record.ID, slugLongMemoryTitle(record.Title)))
	if err := validateLongMemoryRecord(record, true); err != nil {
		return operationresult.Result[longMemoryRecord]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	written, err := s.writeRecordFileLocked(ctx, record)
	if err != nil {
		return operationresult.Result[longMemoryRecord]{}, err
	}
	if written.Status != operationresult.Success {
		return operationresult.Result[longMemoryRecord]{Status: written.Status, Message: written.Message}, nil
	}
	info, err := os.Stat(record.FilePath)
	if err != nil {
		return operationresult.Result[longMemoryRecord]{}, fmt.Errorf("stat long memory file %q: %w", record.FilePath, err)
	}
	record.FileMTimeNS, record.FileSize = info.ModTime().UnixNano(), info.Size()
	if err := s.indexRecordLocked(ctx, record); err != nil {
		return operationresult.Result[longMemoryRecord]{}, err
	}
	return operationresult.Result[longMemoryRecord]{Status: operationresult.Success, Data: record}, nil
}

func (s *longMemoryStore) update(ctx context.Context, preview longMemoryUpdatePreview) (operationresult.Result[longMemoryRecord], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[longMemoryRecord]{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.recordByIDLocked(ctx, preview.Before.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return operationresult.Result[longMemoryRecord]{Status: operationresult.NotFound, Message: fmt.Sprintf("未找到 ID 为 %d 的长期记忆", preview.Before.ID)}, nil
	}
	if err != nil {
		return operationresult.Result[longMemoryRecord]{}, err
	}
	if record.FilePath != preview.Before.FilePath {
		return operationresult.Result[longMemoryRecord]{Status: operationresult.Conflict, Message: "memory target changed after preflight"}, nil
	}
	committed, err := preview.File.Commit(ctx)
	if err != nil {
		return operationresult.Result[longMemoryRecord]{}, err
	}
	if committed.Status != operationresult.Success {
		return operationresult.Result[longMemoryRecord]{Status: committed.Status, Message: committed.Message}, nil
	}
	info, err := os.Stat(preview.After.FilePath)
	if err != nil {
		return operationresult.Result[longMemoryRecord]{}, fmt.Errorf("stat long memory file %q: %w", preview.After.FilePath, err)
	}
	preview.After.FileMTimeNS, preview.After.FileSize = info.ModTime().UnixNano(), info.Size()
	if err := s.indexRecordLocked(ctx, preview.After); err != nil {
		return operationresult.Result[longMemoryRecord]{}, err
	}
	return operationresult.Result[longMemoryRecord]{Status: operationresult.Success, Data: preview.After}, nil
}

func (s *longMemoryStore) previewUpdate(ctx context.Context, args longMemoryUpdateArgs) (operationresult.Result[longMemoryUpdatePreview], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[longMemoryUpdatePreview]{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.previewUpdateLocked(ctx, args)
}

func (s *longMemoryStore) previewUpdateLocked(ctx context.Context, args longMemoryUpdateArgs) (operationresult.Result[longMemoryUpdatePreview], error) {
	if args.ID <= 0 {
		return operationresult.Result[longMemoryUpdatePreview]{Status: operationresult.InvalidArguments, Message: "id is required"}, nil
	}
	if args.Content != nil && len(args.ContentEdits) > 0 {
		return operationresult.Result[longMemoryUpdatePreview]{Status: operationresult.InvalidArguments, Message: "content and content_edits cannot be used together"}, nil
	}
	if !args.hasChanges() {
		return operationresult.Result[longMemoryUpdatePreview]{Status: operationresult.InvalidArguments, Message: "update requires at least one changed field or content_edits"}, nil
	}
	ready, err := s.ensureLocked(ctx)
	if err != nil {
		return operationresult.Result[longMemoryUpdatePreview]{}, err
	}
	if ready.Status != operationresult.Success {
		return operationresult.Result[longMemoryUpdatePreview]{Status: ready.Status, Message: ready.Message}, nil
	}
	record, err := s.recordByIDLocked(ctx, args.ID)
	if errors.Is(err, sql.ErrNoRows) {
		if syncErr := s.syncLocked(ctx, true); syncErr != nil {
			return operationresult.Result[longMemoryUpdatePreview]{}, syncErr
		}
		record, err = s.recordByIDLocked(ctx, args.ID)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return operationresult.Result[longMemoryUpdatePreview]{Status: operationresult.NotFound, Message: fmt.Sprintf("未找到 ID 为 %d 的长期记忆", args.ID)}, nil
		}
		return operationresult.Result[longMemoryUpdatePreview]{}, err
	}
	var preview longMemoryUpdatePreview
	file, err := fileops.PrepareFile(ctx, record.FilePath, func(text string) (operationresult.Result[string], error) {
		parsed, err := parseLongMemoryText(text)
		if err != nil {
			return operationresult.Result[string]{Status: operationresult.Rejected, Message: fmt.Sprintf("无法更新：记忆文件格式损坏，工具不会覆盖原文件。\n%s", longMemoryRepairAdvice(record.FilePath, err))}, nil
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
				return operationresult.Result[string]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
			}
			old := next.Content
			next.Content = strings.TrimSpace(content)
			changes = appendLongMemoryChange(changes, "正文", old, next.Content)
		}
		if err := validateLongMemoryRecord(next, true); err != nil {
			return operationresult.Result[string]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
		}
		if len(changes) == 0 {
			return operationresult.Result[string]{Status: operationresult.Rejected, Message: "edit produced no changes"}, nil
		}
		next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		data, err := marshalLongMemoryFile(next)
		if err != nil {
			return operationresult.Result[string]{}, err
		}
		preview.Before, preview.After, preview.Changes = record, next, changes
		return operationresult.Result[string]{Status: operationresult.Success, Data: string(data)}, nil
	})
	if err != nil {
		return operationresult.Result[longMemoryUpdatePreview]{}, err
	}
	if file.Status != operationresult.Success {
		return operationresult.Result[longMemoryUpdatePreview]{Status: file.Status, Message: file.Message}, nil
	}
	preview.File = file.Data
	return operationresult.Result[longMemoryUpdatePreview]{Status: operationresult.Success, Data: preview}, nil
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

func (s *longMemoryStore) delete(ctx context.Context, approved longMemoryRecord, target, revision string) (operationresult.Result[string], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[string]{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.recordByIDLocked(ctx, approved.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return operationresult.Result[string]{Status: operationresult.NotFound, Message: fmt.Sprintf("未找到 ID 为 %d 的长期记忆", approved.ID)}, nil
	}
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if record.FilePath != approved.FilePath {
		return operationresult.Result[string]{Status: operationresult.Conflict, Message: "memory target changed after preparation"}, nil
	}
	currentTarget, err := filepath.EvalSymlinks(record.FilePath)
	if errors.Is(err, os.ErrNotExist) {
		return operationresult.Result[string]{Status: operationresult.NotFound, Message: "memory file no longer exists"}, nil
	}
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if currentTarget != target {
		return operationresult.Result[string]{Status: operationresult.Conflict, Message: "memory target changed after preparation"}, nil
	}
	data, err := os.ReadFile(record.FilePath)
	if errors.Is(err, os.ErrNotExist) {
		return operationresult.Result[string]{Status: operationresult.NotFound, Message: "memory file no longer exists"}, nil
	}
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if fileops.ContentRevision(data) != revision {
		return operationresult.Result[string]{Status: operationresult.Conflict, Message: "memory changed after preparation"}, nil
	}
	if err := os.Remove(record.FilePath); errors.Is(err, os.ErrNotExist) {
		return operationresult.Result[string]{Status: operationresult.NotFound, Message: "memory file no longer exists"}, nil
	} else if err != nil {
		return operationresult.Result[string]{}, fmt.Errorf("delete memory file %q: %w", record.FilePath, err)
	}
	if err := s.deleteIndexLocked(ctx, record.ID, record.FilePath); err != nil {
		return operationresult.Result[string]{}, err
	}
	return operationresult.Result[string]{Status: operationresult.Success, Data: record.FilePath}, nil
}
