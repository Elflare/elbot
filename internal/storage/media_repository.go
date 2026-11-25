package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func (r *Supervisor) GetSignedURL(ctx context.Context, mediaID string) (*MediaSignedURL, error) {
	record := &MediaSignedURL{MediaID: mediaID}
	var expiresAt string
	err := r.db.QueryRowContext(ctx, `SELECT url, expires_at FROM media_signed_urls WHERE media_id=?`, mediaID).Scan(&record.URL, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get media signed URL: %w", err)
	}
	record.ExpiresAt, err = ParseTime(expiresAt)
	if err != nil {
		return nil, fmt.Errorf("parse media signed URL expiry: %w", err)
	}
	return record, nil
}

func (r *Supervisor) UpsertSignedURL(ctx context.Context, record *MediaSignedURL) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO media_signed_urls (media_id, url, expires_at) VALUES (?, ?, ?) ON CONFLICT(media_id) DO UPDATE SET url=excluded.url, expires_at=excluded.expires_at`, record.MediaID, record.URL, FormatTime(record.ExpiresAt))
	if err != nil {
		return fmt.Errorf("save media signed URL: %w", err)
	}
	return nil
}

func (r *Supervisor) GetMedia(ctx context.Context, id string) (*Media, error) {
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
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get media: %w", err)
	}
	m.Name, m.LocalPath, m.ObjectKey = name.String, localPath.String, objectKey.String
	m.SourcePlatform, m.SourceURL, m.SourceFileID = sourcePlatform.String, sourceURL.String, sourceFileID.String
	m.CreatedAt, _ = ParseTime(createdAt.String)
	m.LastAccessedAt, _ = ParseTime(lastAccessedAt.String)
	if expiresAt.Valid {
		value, err := ParseTime(expiresAt.String)
		if err == nil {
			m.ExpiresAt = &value
		}
	}
	return &m, nil
}

func (r *Supervisor) UpsertMedia(ctx context.Context, m *Media) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = Now()
	}
	if m.LastAccessedAt.IsZero() {
		m.LastAccessedAt = m.CreatedAt
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO media (id, name, mime_type, size, local_path, backend, object_key, source_platform, source_url, source_file_id, created_at, last_accessed_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET name=excluded.name, mime_type=excluded.mime_type, size=excluded.size, local_path=excluded.local_path, backend=excluded.backend, object_key=excluded.object_key, source_platform=excluded.source_platform, source_url=excluded.source_url, source_file_id=excluded.source_file_id, last_accessed_at=excluded.last_accessed_at, expires_at=excluded.expires_at WHERE media.deleting=0`, m.ID, nullString(m.Name), m.MIMEType, m.Size, nullString(m.LocalPath), m.Backend, nullString(m.ObjectKey), nullString(m.SourcePlatform), nullString(m.SourceURL), nullString(m.SourceFileID), FormatTime(m.CreatedAt), FormatTime(m.LastAccessedAt), nullTime(m.ExpiresAt))
	if err != nil {
		return fmt.Errorf("upsert media: %w", err)
	}
	return nil
}

func (r *Supervisor) AddMediaReference(ctx context.Context, ref *MediaReference) error {
	if ref.CreatedAt.IsZero() {
		ref.CreatedAt = Now()
	}
	return r.AddMediaReferences(ctx, []MediaReference{*ref})
}

func (r *Supervisor) AddMediaReferences(ctx context.Context, refs []MediaReference) error {
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
			refs[i].CreatedAt = Now()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_references (media_id, owner_type, owner_id, purpose, session_id, created_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(media_id, owner_type, owner_id, purpose) DO NOTHING`, refs[i].MediaID, refs[i].OwnerType, refs[i].OwnerID, refs[i].Purpose, nullString(refs[i].SessionID), FormatTime(refs[i].CreatedAt)); err != nil {
			// 本语句唯一外键指向 media，唯一拒绝插入的触发器保护 deleting 对象。
			var dbErr *sqlitedriver.Error
			if errors.As(err, &dbErr) && (dbErr.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY || dbErr.Code() == sqlite3.SQLITE_CONSTRAINT_TRIGGER) {
				return fmt.Errorf("add media reference: %w: %v", ErrNotFound, err)
			}
			return fmt.Errorf("add media reference: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit media reference batch: %w", err)
	}
	return nil
}

func (r *Supervisor) RemoveMediaReference(ctx context.Context, ref MediaReference) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM media_references WHERE media_id=? AND owner_type=? AND owner_id=? AND purpose=?`, ref.MediaID, ref.OwnerType, ref.OwnerID, ref.Purpose)
	return err
}
func (r *Supervisor) ListByOwner(ctx context.Context, ownerType, ownerID string) ([]MediaReference, error) {
	return r.list(ctx, `owner_type=? AND owner_id=?`, ownerType, ownerID)
}
func (r *Supervisor) ListMediaIDs(ctx context.Context, mediaID string) ([]MediaReference, error) {
	return r.list(ctx, `media_id=?`, mediaID)
}
func (r *Supervisor) list(ctx context.Context, where string, args ...any) ([]MediaReference, error) {
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
		ref.CreatedAt, _ = ParseTime(createdAt.String)
		out = append(out, ref)
	}
	return out, rows.Err()
}
