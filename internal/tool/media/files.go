// 管理调用私有目录、媒体文件导入导出及完整写入。
package media

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"elbot/internal/media"
	"elbot/internal/tool/share"
	"elbot/internal/workspace"
)

// Workspace 返回相对 base 的调用私有目录，保持 Skill 脚本的相对路径语义。
func (c *MediaCall) Workspace(base string) (string, error) {
	if c.dir == "" {
		dir, usageID, err := c.runtime.center.Workspace(base)
		if err != nil {
			return "", err
		}
		c.dir, c.dirUsage = dir, usageID
	}
	return filepath.Rel(base, c.dir)
}

func mediaFilename(metadata share.MediaInfo) string {
	ext := filepath.Ext(metadata.Name)
	for _, ch := range strings.TrimPrefix(ext, ".") {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9') {
			ext = ""
			break
		}
	}
	return strings.TrimPrefix(metadata.ID, media.IDPrefix) + ext
}

// Export 将持有的媒体写入调用私有目录，返回相对 base 的路径和通用信息。
func (c *MediaCall) Export(ctx context.Context, id, base string) (string, share.MediaInfo, error) {
	metadata, err := c.Retain(ctx, id)
	if err != nil {
		return "", share.MediaInfo{}, err
	}
	relative, err := c.Workspace(base)
	if err != nil {
		return "", share.MediaInfo{}, err
	}
	name := mediaFilename(metadata)
	if c.mediaByID[id].exported {
		return filepath.Join(relative, name), metadata, nil
	}
	root, err := os.OpenRoot(c.dir)
	if err != nil {
		return "", share.MediaInfo{}, err
	}
	defer root.Close()
	if err := c.write(ctx, root, name, c.mediaByID[id].held); err != nil {
		return "", share.MediaInfo{}, err
	}
	c.mediaByID[id].exported = true
	return filepath.Join(relative, name), metadata, nil
}

// Import 规范化受限相对路径，再通过 os.Root 打开普通文件并持有导入结果。
func (c *MediaCall) Import(ctx context.Context, base, path string, input share.MediaImportOptions) (share.MediaInfo, error) {
	path, err := workspace.NormalizeRestrictedPath(path)
	if err != nil {
		return share.MediaInfo{}, err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return share.MediaInfo{}, err
	}
	defer root.Close()
	file, err := root.Open(path)
	if err != nil {
		return share.MediaInfo{}, fmt.Errorf("open media file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return share.MediaInfo{}, err
	}
	if !info.Mode().IsRegular() {
		return share.MediaInfo{}, fmt.Errorf("media must be a regular file")
	}
	if input.Name == "" {
		input.Name = filepath.Base(path)
	}
	metadata, err := c.runtime.center.ImportReader(ctx, file, info.Size(), mediaInput(input), media.ImportLimits{})
	if err != nil {
		return share.MediaInfo{}, err
	}
	return c.retain(ctx, metadata)
}

func (c *MediaCall) write(ctx context.Context, root *os.Root, name string, held *media.HeldMedia) (err error) {
	reader, err := held.Open(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, reader.Close()) }()
	temporary := name + "." + rand.Text() + ".tmp"
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	_, copyErr := io.Copy(file, reader)
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return err
	}
	return root.Rename(temporary, name)
}
