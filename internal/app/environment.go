package app

import (
	"fmt"
	"io"

	"elbot/internal/config"
)

// RunMode 表示本地应用的运行方式。
type RunMode string

const (
	// RunModeTUI 启动本地交互界面、已启用平台和后台任务。
	RunModeTUI RunMode = "tui"
	// RunModeService 以无本地交互界面的服务方式运行。
	RunModeService RunMode = "service"
)

// prepareEnvironment 定位配置文件；服务运行占用交给统一退出流程释放。
func prepareEnvironment(configPath string, mode RunMode) (resolvedConfigPath string, serviceLock io.Closer, err error) {
	switch mode {
	case RunModeTUI, RunModeService:
	default:
		return "", nil, fmt.Errorf("unknown run mode %q", mode)
	}
	resolvedConfigPath, err = config.ResolvePath(configPath)
	if err != nil {
		return "", nil, fmt.Errorf("resolve config path: %w", err)
	}
	// TODO: Service 模式按 resolvedConfigPath 取得跨进程独占运行占用，
	// 将句柄交还 Run；TUI 模式不申请。当前尚不提供服务互斥保证。
	return resolvedConfigPath, nil, nil
}
