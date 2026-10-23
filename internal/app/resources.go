package app

import (
	"context"

	"elbot/internal/config"
	"elbot/internal/logging"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/security"
	"elbot/internal/storage"
)

// 本文件负责按已定位的路径加载配置，准备基础资源和共享服务。
// 共享存储的创建与关闭由 App 协调，各业务线和共享服务管理各自的数据读写规则。
// 平台聊天历史及其存储生命周期归平台总管，通过 wiring.go 连接对外能力。

// sharedResources 持有本次运行的配置、基础资源和共享服务。
// 准备失败时仍保留已取得的资源，尚未创建的字段为空，交由退出协调统一清理。
// 权限管理使用根包的目标类型 security.Permission，下层类型声明待后续调整。
type sharedResources struct {
	config     *config.Config       // 按环境阶段确定的路径加载的配置。
	logs       *logging.Manager     // 日志管理，由 App 协调其生命周期。
	store      storage.Store        // 共享存储，由 App 创建并在使用方退出后关闭。
	media      *media.Manager       // 供业务线共享的媒体管理。
	permission *security.Permission // 供业务线共享的权限管理。
	models     *modelmgr.Service    // 模型管理，从配置取得输入，拥有并持久化自身状态。
}

// prepareResources 加载配置并准备本次运行的基础资源与共享服务。
// resolvedConfigPath 来自 prepareEnvironment，资源阶段不再定位配置路径。
// 成功时返回完整资源；失败时返回已取得的资源与错误，由 Run 发起统一清理。
func prepareResources(
	ctx context.Context,
	resolvedConfigPath string,
) (*sharedResources, error) {
	// 加载已定位的配置，准备日志、共享存储、媒体、权限和模型管理。
	// 将已取得的资源记录到返回集合，供接线使用或在失败后统一清理。
	// 任一步骤失败时停止继续准备，保留部分结果和错误交还 Run。
	pass
}
