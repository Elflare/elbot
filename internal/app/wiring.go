package app

import "context"

// 本文件负责五条业务线总管的创建与公开能力接线。
// 只使用总管和共享服务的公开能力，下层构造与内部接线由各自负责。
// 所有总管完成构造和接线后，由 entry.go 组织启动。

// buildBusinessLines 创建五条业务线总管，并组织公开能力接线。
// resources 是已准备的共享资源，mode 是明确选定的本地运行模式。
// 成功时返回完整业务线；构造或接线失败时仍返回已创建的部分与错误。
// 部分结果由 Run 交给退出协调统一清理，本函数不自行关闭总管。
func buildBusinessLines(
	ctx context.Context,
	resources *sharedResources,
	mode RunMode,
) (*businessLines, error) {
	// 根据依赖关系创建五条业务线总管，将已创建的总管记入返回集合。
	// 调用 connectBusinessLines 连接总管与共享服务的公开能力。
	// 任一步骤失败时保留部分结果与错误交还 Run，全部成功后交还完整集合。
	pass
}

// connectBusinessLines 连接已有总管之间及总管与共享服务之间的公开能力。
// 包括平台聊天历史向工具、媒体和后台提供的查询或清理能力。
// 接线产生的订阅等对象由对应总管或服务持有和释放，App 不另行集中持有。
// 接线失败时已有连接随所属总管或服务统一清理，本函数不启动业务线。
func connectBusinessLines(
	lines *businessLines,
	resources *sharedResources,
) error {
	// 使用总管与共享服务的公开能力建立所需连接。
	// 将订阅等连接资源交由对应使用方持有，供其退出时释放。
	// 接线失败时停止继续连接并返回错误，交由构建阶段连同部分总管上交。
	pass
}
