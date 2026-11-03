// definition.go 定义适配器向 Platform 根部提供的静态声明。
package share

import (
	"elbot/internal/config"
	"elbot/internal/events"
)

// Definition 不依赖适配器实例；名字归根部登记项，禁用时仍提供能力与配置事实。
type Definition struct {
	PrivateSessionIDCode       string                                // 私聊及其他直接会话的短码，也用于后台来源前缀。
	GroupSessionIDCode         string                                // 群聊及频道的短码，按声明原样使用。
	ConfigDefinition           config.Definition                     // 不创建适配器即可取得的配置描述。
	ValidateNotificationTarget func(events.NotificationTarget) error // 通知入队前执行的目标规则，注册后不再检查函数是否存在。
}
