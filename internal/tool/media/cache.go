// 使用配置根目录下的共享媒体缓存；保留期清理由维护任务负责。
package media

import (
	"context"
	"elbot/internal/operationresult"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"elbot/internal/workspace"
)

// CachedExport 首次使用时刷新 ModTime，同次调用复用已准备的路径。
func (c *MediaCall) CachedExport(ctx context.Context, id string) (operationresult.Result[string], error) {
	metadata, err := c.Retain(ctx, id)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if metadata.Status != operationresult.Success {
		return operationresult.Result[string]{Status: metadata.Status, Message: metadata.Message}, nil
	}
	if c.mediaByID[id].cachedPath != "" {
		return operationresult.Result[string]{Status: operationresult.Success, Data: c.mediaByID[id].cachedPath}, nil
	}
	base, err := workspace.SandboxDir(c.runtime.sandboxRoot, ".")
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return operationresult.Result[string]{}, err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	defer root.Close()
	if err := root.MkdirAll("media-inputs", 0700); err != nil {
		return operationresult.Result[string]{}, err
	}
	name := filepath.Join("media-inputs", mediaFilename(metadata.Data))
	info, err := root.Lstat(name)
	if err == nil {
		if !info.Mode().IsRegular() {
			return operationresult.Result[string]{}, fmt.Errorf("media cache is not a regular file")
		}
	} else if !os.IsNotExist(err) {
		return operationresult.Result[string]{}, err
	} else if err := write(ctx, root, name, c.mediaByID[id].held); err != nil {
		return operationresult.Result[string]{}, err
	}
	now := time.Now()
	if err := root.Chtimes(name, now, now); err != nil {
		return operationresult.Result[string]{}, err
	}
	c.mediaByID[id].cachedPath = filepath.ToSlash(filepath.Join(base, name))
	return operationresult.Result[string]{Status: operationresult.Success, Data: c.mediaByID[id].cachedPath}, nil
}
