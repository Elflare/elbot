// Package backend 提供对象位置上的读写及远端签名。
package backend

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"elbot/internal/media/share"
)

// LocalBackend 只管理本地内容，不提供远端签名。
type LocalBackend struct {
	Root string // 本地内容根目录，由 Media 根部确定。
}

// Put 按内容 ID 写入本地对象，返回实际文件位置。
// 文件写入同步执行，完成写入后在提交前检查取消。
func (b *LocalBackend) Put(ctx context.Context, id string, input io.Reader, size int64, contentType string) (location string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path := b.path(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create media directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".media-*")
	if err != nil {
		return "", fmt.Errorf("create media temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, input); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write media: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close media temporary file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("store media: %w", err)
	}
	return path, nil
}

// Open 打开已由对象管理选定的位置，不接收存储记录。
func (b *LocalBackend) Open(ctx context.Context, location string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(location)
	if err != nil {
		return nil, fmt.Errorf("open media: %w", err)
	}
	return file, nil
}

// Remove 删除本地对象位置，已不存在视为完成。
func (b *LocalBackend) Remove(ctx context.Context, location string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(location); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove media: %w", err)
	}
	return nil
}

// path 根据内容标识确定本地对象位置。
func (b *LocalBackend) path(id string) string {
	hash := id[len(share.IDPrefix):]
	return filepath.Join(b.Root, hash[:2], hash[2:])
}

var _ share.Backend = (*LocalBackend)(nil)
