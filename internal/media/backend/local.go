// Package backend 提供对象位置上的读写及远端签名；当前仅为声明骨架。
package backend

import (
	"context"
	"elbot/internal/media/share"
	"io"
)

// LocalBackend 只管理本地内容，不提供远端签名。
type LocalBackend struct {
	Root string // 本地内容根目录，由 Media 根部确定。
}

// Put 按内容 ID 写入本地对象，返回实际文件位置。
func (b *LocalBackend) Put(ctx context.Context, id string, input io.Reader, size int64, contentType string) (location string, err error) {
	panic("TODO")
}

// Open 打开已由对象管理选定的位置，不接收存储记录。
func (b *LocalBackend) Open(ctx context.Context, location string) (io.ReadCloser, error) {
	panic("TODO")
}

// Remove 删除本地对象位置，已不存在视为完成。
func (b *LocalBackend) Remove(ctx context.Context, location string) error { panic("TODO") }

// path 根据内容标识确定本地对象位置。
func (b *LocalBackend) path(id string) string { panic("TODO") }

var _ share.Backend = (*LocalBackend)(nil)
