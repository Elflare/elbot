package app

import (
	"fmt"
	"io"
	"path/filepath"

	"elbot/internal/config"

	"github.com/gofrs/flock"
)

// RunMode 表示本地应用的运行方式。
type RunMode string

const (
	// RunModeTUI 启动本地交互界面、已启用平台和后台任务。
	RunModeTUI RunMode = "tui"
	// RunModeService 以无本地交互界面的服务方式运行。
	RunModeService RunMode = "service"
)

// prepareEnvironment 定位配置文件并取得本地实例独占锁，由统一退出流程释放。
func prepareEnvironment(configPath string, mode RunMode) (string, io.Closer, error) {
	switch mode {
	case RunModeTUI, RunModeService:
	default:
		return "", nil, fmt.Errorf("unknown run mode %q", mode)
	}
	resolvedConfigPath, err := config.ResolvePath(configPath)
	if err != nil {
		return "", nil, fmt.Errorf("resolve config path: %w", err)
	}
	resolvedConfigPath, err = filepath.Abs(resolvedConfigPath)
	if err != nil {
		return "", nil, fmt.Errorf("make config path absolute: %w", err)
	}
	resolvedConfigPath, err = filepath.EvalSymlinks(resolvedConfigPath)
	if err != nil {
		return "", nil, fmt.Errorf("resolve config symlinks: %w", err)
	}

	// 独立锁文件避免配置替换保存时丢失互斥；释放锁时保留该文件。
	lockPath := resolvedConfigPath + ".lock"
	configLock := flock.New(lockPath)
	locked, err := configLock.TryLock()
	if err != nil {
		return "", nil, fmt.Errorf("acquire config lock %q: %w", lockPath, err)
	}
	if !locked {
		return "", nil, fmt.Errorf("config %q is already in use by another local instance", resolvedConfigPath)
	}
	return resolvedConfigPath, configLock, nil
}
