// 管理长期记忆索引数据库、文件同步和索引读写。
package long_memory

import (
	"context"
	"database/sql"
	"elbot/internal/operationresult"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const longMemorySyncCooldown = 30 * time.Second

type longMemoryInvalidFile struct {
	FilePath    string
	Error       string
	DetectedAt  string
	FileMTimeNS int64 // 发现格式错误时的文件修改时间，单位为纳秒。
	FileSize    int64 // 发现格式错误时的文件字节数。
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
	s.lastSync = time.Time{}
	s.formatErrors = nil
	return err
}

func (s *longMemoryStore) ensureLocked(ctx context.Context) (operationresult.Result[struct{}], error) {
	if s.initialized {
		return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	}
	if strings.TrimSpace(s.rootDir) == "" {
		return operationresult.Result[struct{}]{Status: operationresult.Unavailable, Message: "long memory directory is not configured"}, nil
	}
	if err := os.MkdirAll(s.memoriesDir, 0o755); err != nil {
		return operationresult.Result[struct{}]{}, fmt.Errorf("create long memory directory %q: %w", s.memoriesDir, err)
	}
	db, err := sql.Open("sqlite", s.indexPath)
	if err != nil {
		return operationresult.Result[struct{}]{}, fmt.Errorf("open long memory index: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return operationresult.Result[struct{}]{}, fmt.Errorf("enable long memory foreign keys: %w", err)
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
		return operationresult.Result[struct{}]{}, fmt.Errorf("create long memory metadata table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS memory_invalid_files (
		file_path TEXT PRIMARY KEY,
		error TEXT NOT NULL,
		detected_at TEXT NOT NULL,
		file_mtime_ns INTEGER NOT NULL,
		file_size INTEGER NOT NULL
	)`); err != nil {
		_ = db.Close()
		return operationresult.Result[struct{}]{}, fmt.Errorf("create long memory invalid table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(category, title, summary, keywords, content_tokens, content_id UNINDEXED)`); err != nil {
		_ = db.Close()
		return operationresult.Result[struct{}]{}, fmt.Errorf("create long memory FTS table: %w", err)
	}
	s.db = db
	s.initialized = true
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func (s *longMemoryStore) syncLocked(ctx context.Context, force bool) error {
	if !force && !s.lastSync.IsZero() && time.Since(s.lastSync) < longMemorySyncCooldown {
		return nil
	}
	// A failed forced sync must not leave a previous cooldown blocking retries.
	s.lastSync = time.Time{}
	entries, err := os.ReadDir(s.memoriesDir)
	if err != nil {
		return fmt.Errorf("read long memory directory %q: %w", s.memoriesDir, err)
	}
	indexed, err := s.indexedStatesLocked(ctx)
	if err != nil {
		return err
	}
	invalids, err := s.invalidStatesLocked(ctx)
	if err != nil {
		return err
	}
	seen := map[string]longMemoryRecord{}
	seenIDs := map[int64]string{}
	formatErrors := map[string]longMemoryInvalidFile{}
	invalidFiles := map[string]longMemoryInvalidFile{}
	var changed []longMemoryRecord
	// ReadDir sorts names. Decide all ID ownership before changing the old index.
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		path := filepath.Join(s.memoriesDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat long memory file %q: %w", path, err)
		}
		mtimeNS := info.ModTime().UnixNano()
		size := info.Size()
		invalid, hasInvalid := invalids[path]
		if cached, ok := s.formatErrors[path]; ok && hasInvalid && cached == invalid && invalid.FileMTimeNS == mtimeNS && invalid.FileSize == size {
			formatErrors[path] = invalid
			invalidFiles[path] = invalid
			continue
		}
		record, exists := indexed[path]
		unchanged := exists && !hasInvalid && record.FileMTimeNS == mtimeNS && record.FileSize == size
		if !unchanged {
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read long memory file %q: %w", path, err)
			}
			parsed, err := parseLongMemoryText(string(data))
			if err == nil {
				record = recordFromParsedFile(path, parsed, mtimeNS, size)
				err = validateLongMemoryRecord(record, false)
			}
			if err != nil {
				invalid = longMemoryInvalidFile{FilePath: path, Error: err.Error(), FileMTimeNS: mtimeNS, FileSize: size}
				formatErrors[path] = invalid
				invalidFiles[path] = invalid
				continue
			}
		}
		if previous, ok := seenIDs[record.ID]; ok && previous != path {
			invalidFiles[path] = longMemoryInvalidFile{FilePath: path, Error: fmt.Sprintf("id %d 与文件 %s 重复", record.ID, previous), FileMTimeNS: mtimeNS, FileSize: size}
			continue
		}
		seenIDs[record.ID] = path
		seen[path] = record
		if !unchanged {
			changed = append(changed, record)
		}
	}
	for path, record := range indexed {
		if seen[path].ID != record.ID {
			if err := s.deleteIndexLocked(ctx, record.ID, path); err != nil {
				return err
			}
			delete(invalids, path)
		}
	}
	for _, record := range changed {
		if err := s.indexRecordLocked(ctx, record); err != nil {
			return err
		}
		delete(invalids, record.FilePath)
	}
	for path := range invalids {
		if _, exists := invalidFiles[path]; !exists {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM memory_invalid_files WHERE file_path = ?`, path); err != nil {
				return fmt.Errorf("delete stale invalid long memory %q: %w", path, err)
			}
		}
	}
	for path, invalid := range invalidFiles {
		previous, exists := invalids[path]
		if exists && previous.Error == invalid.Error && previous.FileMTimeNS == invalid.FileMTimeNS && previous.FileSize == invalid.FileSize {
			invalid = previous
		} else {
			invalid.DetectedAt = time.Now().UTC().Format(time.RFC3339)
			if err := s.upsertInvalidLocked(ctx, invalid); err != nil {
				return err
			}
		}
		if _, exists := formatErrors[path]; exists {
			formatErrors[path] = invalid
		}
	}
	s.formatErrors = formatErrors
	s.lastSync = time.Now()
	return nil
}

func (s *longMemoryStore) indexRecordLocked(ctx context.Context, record longMemoryRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO memory_files (id, file_path, category, title, summary, keywords, created_at, updated_at, file_mtime_ns, file_size)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET file_path=excluded.file_path, category=excluded.category, title=excluded.title, summary=excluded.summary, keywords=excluded.keywords, created_at=excluded.created_at, updated_at=excluded.updated_at, file_mtime_ns=excluded.file_mtime_ns, file_size=excluded.file_size`,
		record.ID, record.FilePath, record.Category, record.Title, record.Summary, record.Keywords, record.CreatedAt, record.UpdatedAt, record.FileMTimeNS, record.FileSize)
	if err != nil {
		return fmt.Errorf("index long memory metadata: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM memory_fts WHERE content_id = ?`, record.ID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO memory_fts (category, title, summary, keywords, content_tokens, content_id) VALUES (?, ?, ?, ?, ?, ?)`, tokenizeLongMemory(record.Category), tokenizeLongMemory(record.Title), tokenizeLongMemory(record.Summary), strings.Join(splitLongMemoryKeywords(record.Keywords), " "), tokenizeLongMemory(record.Content), record.ID); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM memory_invalid_files WHERE file_path = ?`, record.FilePath)
	if err != nil {
		return fmt.Errorf("clear long memory invalid state: %w", err)
	}
	delete(s.formatErrors, record.FilePath)
	return nil
}

func (s *longMemoryStore) deleteIndexLocked(ctx context.Context, id int64, path string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM memory_fts WHERE content_id = ?`, id); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM memory_files WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete long memory metadata: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM memory_invalid_files WHERE file_path = ?`, path); err != nil {
		return fmt.Errorf("delete long memory invalid state: %w", err)
	}
	delete(s.formatErrors, path)
	return nil
}

func (s *longMemoryStore) recordByIDLocked(ctx context.Context, id int64) (longMemoryRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, file_path, category, title, summary, keywords, created_at, updated_at, file_mtime_ns, file_size FROM memory_files WHERE id = ?`, id)
	var record longMemoryRecord
	err := row.Scan(&record.ID, &record.FilePath, &record.Category, &record.Title, &record.Summary, &record.Keywords, &record.CreatedAt, &record.UpdatedAt, &record.FileMTimeNS, &record.FileSize)
	return record, err
}

func (s *longMemoryStore) indexedStatesLocked(ctx context.Context) (map[string]longMemoryRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, file_path, file_mtime_ns, file_size FROM memory_files`)
	if err != nil {
		return nil, fmt.Errorf("list indexed long memory paths: %w", err)
	}
	defer rows.Close()
	records := map[string]longMemoryRecord{}
	for rows.Next() {
		var record longMemoryRecord
		if err := rows.Scan(&record.ID, &record.FilePath, &record.FileMTimeNS, &record.FileSize); err != nil {
			return nil, err
		}
		records[record.FilePath] = record
	}
	return records, rows.Err()
}

func (s *longMemoryStore) invalidStatesLocked(ctx context.Context) (map[string]longMemoryInvalidFile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT file_path, error, detected_at, file_mtime_ns, file_size FROM memory_invalid_files`)
	if err != nil {
		return nil, fmt.Errorf("list invalid long memory paths: %w", err)
	}
	defer rows.Close()
	files := map[string]longMemoryInvalidFile{}
	for rows.Next() {
		var file longMemoryInvalidFile
		if err := rows.Scan(&file.FilePath, &file.Error, &file.DetectedAt, &file.FileMTimeNS, &file.FileSize); err != nil {
			return nil, err
		}
		files[file.FilePath] = file
	}
	return files, rows.Err()
}

func (s *longMemoryStore) upsertInvalidLocked(ctx context.Context, file longMemoryInvalidFile) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO memory_invalid_files (file_path, error, detected_at, file_mtime_ns, file_size)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(file_path) DO UPDATE SET error=excluded.error, detected_at=excluded.detected_at, file_mtime_ns=excluded.file_mtime_ns, file_size=excluded.file_size`, file.FilePath, file.Error, file.DetectedAt, file.FileMTimeNS, file.FileSize)
	if err != nil {
		return fmt.Errorf("record invalid long memory %q: %w", file.FilePath, err)
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
		return nil, err
	}
	defer rows.Close()
	return scanLongMemoryRecords(rows)
}

func (s *longMemoryStore) nextIDLocked(ctx context.Context) (int64, error) {
	var maxID sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(id) FROM memory_files`).Scan(&maxID); err != nil {
		return 0, err
	}
	id := maxID.Int64 + 1
	for {
		pathGlob := filepath.Join(s.memoriesDir, fmt.Sprintf("%06d-*.md", id))
		matches, err := filepath.Glob(pathGlob)
		if err != nil {
			return 0, err
		}
		if len(matches) == 0 {
			return id, nil
		}
		id++
	}
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
