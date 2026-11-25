package object

import (
	"context"
	"errors"
	"fmt"
	"time"

	"elbot/internal/storage"
)

// signedURLRefreshMargin 在到期前刷新缓存，避免交付临近失效的地址。
const signedURLRefreshMargin = 5 * time.Minute

// SignedURL 基于已有持有取得远端 URL，必要时上传并更新对象及签名记录。
func (h *heldMedia) SignedURL(ctx context.Context) (string, error) {
	if h.released.Load() {
		return "", fmt.Errorf("media hold is closed")
	}
	m, id := h.manager, h.record.ID
	unlock, err := m.objects.acquire(ctx, id)
	if err != nil {
		return "", err
	}
	defer unlock()
	// 每次读取当前副本事实，持有快照不随签名更新，允许多个读取同时使用。
	metadata, err := m.media.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if m.remote == nil {
		return "", fmt.Errorf("remote media backend is unavailable")
	}
	if err := m.media.Touch(ctx, id, m.options.Now()); err != nil {
		return "", err
	}
	if metadata.ObjectKey == "" {
		if metadata.Backend != "local" {
			return "", fmt.Errorf("media %q has no remote object key", id)
		}
		reader, err := m.local.Open(ctx, metadata.LocalPath)
		if err != nil {
			return "", err
		}
		location, err := m.remote.Put(ctx, id, reader, metadata.Size, metadata.MIMEType)
		closeMediaReader(ctx, reader)
		if err != nil {
			return "", err
		}
		metadata.ObjectKey = location
		if err := m.media.Upsert(ctx, metadata); err != nil {
			removeUnrecordedObject(m.remote, location)
			return "", err
		}
	}
	cached, err := m.media.GetSignedURL(ctx, id)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return "", err
	}
	now := m.options.Now()
	if err == nil && cached.ExpiresAt.After(now.Add(signedURLRefreshMargin)) {
		return cached.URL, nil
	}
	expiresAt := now.Truncate(time.Second).Add(time.Hour)
	url, err := m.remote.PresignGet(ctx, metadata.ObjectKey, time.Hour)
	if err != nil {
		return "", err
	}
	if err := m.media.UpsertSignedURL(ctx, &storage.MediaSignedURL{MediaID: id, URL: url, ExpiresAt: expiresAt}); err != nil {
		return "", err
	}
	return url, nil
}
