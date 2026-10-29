// 职责：定义会话管理组件、自有配置及所需依赖。
// 接收绑定协调、存储、活跃状态和前台接管校验能力。
// TODO(session重构)：实现 share.BindingCoordinator，统一承接绑定切换后的计时和退役，底层绑定委托 binding。
// 根部注入 IsSuperadmin、ReleaseSession、ReleaseBinding；无其他绑定且无活动工作时才释放会话临时状态。
// 必需释放为准入内的同步内存操作，不做 I/O、不等待任务、不再次获取准入，也不依赖 BindingChanged 成功。
package control
