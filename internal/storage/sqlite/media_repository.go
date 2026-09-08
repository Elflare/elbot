package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"elbot/internal/storage"
)

type MediaRepository struct{ db *sql.DB }

type MediaReferenceRepository struct{ db *sql.DB }

func (r *MediaRepository) Get(ctx context.Context, id string) (*storage.Media, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id, name, mime_type, size, local_path, backend, object_key, source_platform, source_url, source_file_id, created_at, last_accessed_at, expires_at FROM media WHERE id = ?`, id)
	var m storage.Media
	var name, localPath, objectKey, sourcePlatform, sourceURL, sourceFileID, createdAt, lastAccessedAt string
	var expiresAt sql.NullString
	if err := row.Scan(&m.ID, &name, &m.MIMEType, &m.Size, &localPath, &m.Backend, &objectKey, &sourcePlatform, &sourceURL, &sourceFileID, &createdAt, &lastAccessedAt, &expiresAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("get media: %w", err)
	}
	m.Name, m.LocalPath, m.ObjectKey = name, localPath, objectKey
	m.SourcePlatform, m.SourceURL, m.SourceFileID = sourcePlatform, sourceURL, sourceFileID
	m.CreatedAt, _ = storage.ParseTime(createdAt)
	m.LastAccessedAt, _ = storage.ParseTime(lastAccessedAt)
	if expiresAt.Valid {
		value, err := storage.ParseTime(expiresAt.String)
		if err == nil {
			m.ExpiresAt = &value
		}
	}
	return &m, nil
}

func (r *MediaRepository) Upsert(ctx context.Context, m *storage.Media) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = storage.Now()
	}
	if m.LastAccessedAt.IsZero() {
		m.LastAccessedAt = m.CreatedAt
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO media (id, name, mime_type, size, local_path, backend, object_key, source_platform, source_url, source_file_id, created_at, last_accessed_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET name=excluded.name, mime_type=excluded.mime_type, size=excluded.size, local_path=excluded.local_path, backend=excluded.backend, object_key=excluded.object_key, source_platform=excluded.source_platform, source_url=excluded.source_url, source_file_id=excluded.source_file_id, last_accessed_at=excluded.last_accessed_at, expires_at=excluded.expires_at`, m.ID, nullString(m.Name), m.MIMEType, m.Size, nullString(m.LocalPath), m.Backend, nullString(m.ObjectKey), nullString(m.SourcePlatform), nullString(m.SourceURL), nullString(m.SourceFileID), storage.FormatTime(m.CreatedAt), storage.FormatTime(m.LastAccessedAt), nullableTime(m.ExpiresAt))
	if err != nil {
		return fmt.Errorf("upsert media: %w", err)
	}
	return nil
}

func (r *MediaRepository) DeleteOrphans(ctx context.Context, cutoff time.Time) ([]storage.Media, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, mime_type, size, local_path, backend, object_key, source_platform, source_url, source_file_id, created_at, last_accessed_at, expires_at FROM media WHERE created_at < ? AND NOT EXISTS (SELECT 1 FROM media_references WHERE media_references.media_id = media.id)`, storage.FormatTime(cutoff))
	if err != nil {
		return nil, fmt.Errorf("list orphan media: %w", err)
	}
	defer rows.Close()
	var out []storage.Media
	for rows.Next() {
		var m storage.Media
		var name, localPath, objectKey, sourcePlatform, sourceURL, sourceFileID, createdAt, lastAccessedAt string
		var expiresAt sql.NullString
		if err := rows.Scan(&m.ID, &name, &m.MIMEType, &m.Size, &localPath, &m.Backend, &objectKey, &sourcePlatform, &sourceURL, &sourceFileID, &createdAt, &lastAccessedAt, &expiresAt); err != nil {
			return nil, err
		}
		m.Name, m.LocalPath, m.ObjectKey, m.SourcePlatform, m.SourceURL, m.SourceFileID = name, localPath, objectKey, sourcePlatform, sourceURL, sourceFileID
		m.CreatedAt, _ = storage.ParseTime(createdAt)
		m.LastAccessedAt, _ = storage.ParseTime(lastAccessedAt)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *MediaReferenceRepository) Add(ctx context.Context, ref *storage.MediaReference) error {
	if ref.CreatedAt.IsZero() {
		ref.CreatedAt = storage.Now()
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO media_references (media_id, owner_type, owner_id, purpose, session_id, created_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(media_id, owner_type, owner_id, purpose) DO NOTHING`, ref.MediaID, ref.OwnerType, ref.OwnerID, ref.Purpose, nullString(ref.SessionID), storage.FormatTime(ref.CreatedAt))
	if err != nil {
		return fmt.Errorf("add media reference: %w", err)
	}
	return nil
}

func (r *MediaReferenceRepository) Remove(ctx context.Context, ref storage.MediaReference) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM media_references WHERE media_id=? AND owner_type=? AND owner_id=? AND purpose=?`, ref.MediaID, ref.OwnerType, ref.OwnerID, ref.Purpose)
	return err
}
func (r *MediaReferenceRepository) ListByOwner(ctx context.Context, ownerType, ownerID string) ([]storage.MediaReference, error) {
	return r.list(ctx, `owner_type=? AND owner_id=?`, ownerType, ownerID)
}
func (r *MediaReferenceRepository) ListMediaIDs(ctx context.Context, mediaID string) ([]storage.MediaReference, error) {
	return r.list(ctx, `media_id=?`, mediaID)
}
func (r *MediaReferenceRepository) list(ctx context.Context, where string, args ...any) ([]storage.MediaReference, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT media_id, owner_type, owner_id, purpose, session_id, created_at FROM media_references WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storage.MediaReference
	for rows.Next() {
		var ref storage.MediaReference
		var sessionID, createdAt sql.NullString
		if err := rows.Scan(&ref.MediaID, &ref.OwnerType, &ref.OwnerID, &ref.Purpose, &sessionID, &createdAt); err != nil {
			return nil, err
		}
		ref.SessionID = sessionID.String
		ref.CreatedAt, _ = storage.ParseTime(createdAt.String)
		out = append(out, ref)
	}
	return out, rows.Err()
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return storage.FormatTime(*value)
}
