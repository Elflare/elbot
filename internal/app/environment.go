package app

import "io"

// 本文件负责本地应用运行模式、配置路径定位和按配置取得服务运行占用。
// 配置内容加载归 resources.go；服务运行占用交由统一退出流程释放。

// RunMode 表示本地应用的运行方式。
type RunMode string

const (
	// RunModeTUI 启动本地交互界面、已启用平台和后台任务。
	RunModeTUI RunMode = "tui"

	// RunModeService 以无本地交互界面的服务方式运行。
	RunModeService RunMode = "service"
)

// prepareEnvironment 确定实际配置路径，并准备进程级运行占用。
// configPath 是调用方传入的配置路径，可以为空；mode 由启动分流明确选定。
// resolvedConfigPath 是定位后的实际配置文件路径，交给资源准备阶段加载内容。
// serviceLock 持有对应配置的独占服务运行资格；TUI 模式下为空。
// 同一配置已有服务占用时返回错误，不继续启动第二个服务。
// 返回的占用句柄由 Run 持有，退出时交给 shutdown.go 调用 Close 释放。
func prepareEnvironment(
	configPath string,
	mode RunMode,
) (resolvedConfigPath string, serviceLock io.Closer, err error) {
	// 按显式路径、ELBOT_CONFIG_FILE、平台默认位置的顺序定位实际配置文件。
	// Service 模式取得该配置的独占运行句柄；TUI 模式不申请服务运行占用。
	// 返回实际配置路径、占用句柄和错误；具体占用实现留到填充实现阶段确定。
	pass
}
