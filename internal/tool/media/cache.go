// 使用配置根目录下的共享媒体缓存；保留期清理由维护任务负责。
package media

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"elbot/internal/workspace"
)

// CachedExport 首次使用时刷新 ModTime，同次调用复用已准备的路径。
func (c *MediaCall) CachedExport(ctx context.Context, id string) (string, error) {
	metadata, err := c.Retain(ctx, id)
	if err != nil {
		return "", err
	}
	if c.mediaByID[id].cachedPath != "" {
		return c.mediaByID[id].cachedPath, nil
	}
	base, err := workspace.SandboxDir(c.runtime.sandboxRoot, ".")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := root.MkdirAll("media-inputs", 0700); err != nil {
		return "", err
	}
	name := filepath.Join("media-inputs", mediaFilename(metadata))
	info, err := root.Lstat(name)
	if err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("media cache is not a regular file")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	} else if err := c.write(ctx, root, name, c.mediaByID[id].held); err != nil {
		return "", err
	}
	now := time.Now()
	if err := root.Chtimes(name, now, now); err != nil {
		return "", err
	}
	c.mediaByID[id].cachedPath = filepath.ToSlash(filepath.Join(base, name))
	return c.mediaByID[id].cachedPath, nil
}
