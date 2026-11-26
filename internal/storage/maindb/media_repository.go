package maindb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/storage/share"
	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// MediaSignedURL caches a download URL independently of the media's lifetime.
type MediaSignedURL struct {
	MediaID string
	URL     string
	// ExpiresAt is the URL's absolute expiry, not the media retention deadline.
	ExpiresAt time.Time
}

type Media struct {
	Deleting       bool
	ID             string
	Name           string
	MIMEType       string
	Size           int64
	LocalPath      string
	Backend        string
	ObjectKey      string
	SourcePlatform string
	SourceURL      string
	SourceFileID   string
	CreatedAt      time.Time
	LastAccessedAt time.Time
	ExpiresAt      *time.Time
}

// HistoryMedia associates one ordered media position with an observed chat message.
type HistoryMedia struct {
	HistoryID  string
	Platform   string
	ChatType   contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID     string
	MessageID  string
	MediaIndex int
	Kind       string
	MediaID    string
	OwnerID    string
}
type MediaReference struct {
	MediaID   string
	OwnerType string
	OwnerID   string
	Purpose   string
	SessionID string
	CreatedAt time.Time
}

type MediaOutput struct {
	Platform     string
	ChatType     contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID       string
	MessageID    string
	SegmentIndex int
	Kind         string
	MediaID      string
	OwnerID      string
	ExpiresAt    time.Time
}

func (r *Store) GetSignedURL(ctx context.Context, mediaID string) (*MediaSignedURL, error) {
	record := &MediaSignedURL{MediaID: mediaID}
	var expiresAt string
	err := r.db.QueryRowContext(ctx, `SELECT url, expires_at FROM media_signed_urls WHERE media_id=?`, mediaID).Scan(&record.URL, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, share.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get media signed URL: %w", err)
	}
	record.ExpiresAt, err = share.ParseTime(expiresAt)
	if err != nil {
		return nil, fmt.Errorf("parse media signed URL expiry: %w", err)
	}
	return record, nil
}

func (r *Store) UpsertSignedURL(ctx context.Context, record *MediaSignedURL) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO media_signed_urls (media_id, url, expires_at) VALUES (?, ?, ?) ON CONFLICT(media_id) DO UPDATE SET url=excluded.url, expires_at=excluded.expires_at`, record.MediaID, record.URL, share.FormatTime(record.ExpiresAt))
	if err != nil {
		return fmt.Errorf("save media signed URL: %w", err)
	}
	return nil
}

func (r *Store) GetMedia(ctx context.Context, id string) (*Media, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id, name, mime_type, size, local_path, backend, object_key, source_platform, source_url, source_file_id, created_at, last_accessed_at, expires_at, deleting FROM media WHERE id = ?`, id)
	return scanMedia(row)
}

// scanMedia reads the metadata shared by ordinary reads and orphan claiming.
func scanMedia(row interface{ Scan(...any) error }) (*Media, error) {
	var m Media
	var name, localPath, objectKey, sourcePlatform, sourceURL, sourceFileID, createdAt, lastAccessedAt sql.NullString
	var expiresAt sql.NullString
	if err := row.Scan(&m.ID, &name, &m.MIMEType, &m.Size, &localPath, &m.Backend, &objectKey, &sourcePlatform, &sourceURL, &sourceFileID, &createdAt, &lastAccessedAt, &expiresAt, &m.Deleting); err != nil {
		if err == sql.ErrNoRows {
			return nil, share.ErrNotFound
		}
		return nil, fmt.Errorf("get media: %w", err)
	}
	m.Name, m.LocalPath, m.ObjectKey = name.String, localPath.String, objectKey.String
	m.SourcePlatform, m.SourceURL, m.SourceFileID = sourcePlatform.String, sourceURL.String, sourceFileID.String
	m.CreatedAt, _ = share.ParseTime(createdAt.String)
	m.LastAccessedAt, _ = share.ParseTime(lastAccessedAt.String)
	if expiresAt.Valid {
		value, err := share.ParseTime(expiresAt.String)
		if err == nil {
			m.ExpiresAt = &value
		}
	}
	return &m, nil
}

func (r *Store) CreateMedia(ctx context.Context, m *Media) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO media (id, name, mime_type, size, local_path, backend, object_key, source_platform, source_url, source_file_id, created_at, last_accessed_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, m.ID, share.NullString(m.Name), m.MIMEType, m.Size, share.NullString(m.LocalPath), m.Backend, share.NullString(m.ObjectKey), share.NullString(m.SourcePlatform), share.NullString(m.SourceURL), share.NullString(m.SourceFileID), share.FormatTime(m.CreatedAt), share.FormatTime(m.LastAccessedAt), nullTime(m.ExpiresAt))
	if err != nil {
		return fmt.Errorf("create media: %w", err)
	}
	return nil
}

func (r *Store) SetMediaObjectKey(ctx context.Context, id, key string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE media SET object_key=? WHERE id=? AND deleting=0`, key, id)
	return requireRow(res, err, "set media object key")
}

func (r *Store) AddMediaReference(ctx context.Context, ref *MediaReference) error {
	if ref.CreatedAt.IsZero() {
		ref.CreatedAt = time.Now()
	}
	return r.AddMediaReferences(ctx, []MediaReference{*ref})
}

func (r *Store) AddMediaReferences(ctx context.Context, refs []MediaReference) error {
	if len(refs) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin media reference batch: %w", err)
	}
	defer tx.Rollback()
	for i := range refs {
		if refs[i].CreatedAt.IsZero() {
			refs[i].CreatedAt = time.Now()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_references (media_id, owner_type, owner_id, purpose, session_id, created_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(media_id, owner_type, owner_id, purpose) DO NOTHING`, refs[i].MediaID, refs[i].OwnerType, refs[i].OwnerID, refs[i].Purpose, share.NullString(refs[i].SessionID), share.FormatTime(refs[i].CreatedAt)); err != nil {
			// 本语句唯一外键指向 media，唯一拒绝插入的触发器保护 deleting 对象。
			var dbErr *sqlitedriver.Error
			if errors.As(err, &dbErr) && (dbErr.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY || dbErr.Code() == sqlite3.SQLITE_CONSTRAINT_TRIGGER) {
				return fmt.Errorf("add media reference: %w: %v", share.ErrNotFound, err)
			}
			return fmt.Errorf("add media reference: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit media reference batch: %w", err)
	}
	return nil
}

func (r *Store) RemoveMediaReference(ctx context.Context, ref MediaReference) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM media_references WHERE media_id=? AND owner_type=? AND owner_id=? AND purpose=?`, ref.MediaID, ref.OwnerType, ref.OwnerID, ref.Purpose)
	return err
}
func (r *Store) ListByOwner(ctx context.Context, ownerType, ownerID string) ([]MediaReference, error) {
	return r.list(ctx, `owner_type=? AND owner_id=?`, ownerType, ownerID)
}
func (r *Store) ListMediaIDs(ctx context.Context, mediaID string) ([]MediaReference, error) {
	return r.list(ctx, `media_id=?`, mediaID)
}
func (r *Store) list(ctx context.Context, where string, args ...any) ([]MediaReference, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT media_id, owner_type, owner_id, purpose, session_id, created_at FROM media_references WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MediaReference
	for rows.Next() {
		var ref MediaReference
		var sessionID, createdAt sql.NullString
		if err := rows.Scan(&ref.MediaID, &ref.OwnerType, &ref.OwnerID, &ref.Purpose, &sessionID, &createdAt); err != nil {
			return nil, err
		}
		ref.SessionID = sessionID.String
		ref.CreatedAt, _ = share.ParseTime(createdAt.String)
		out = append(out, ref)
	}
	return out, rows.Err()
}
