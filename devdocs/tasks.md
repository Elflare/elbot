# 架构优化
总体原则：
1. 从上往下层设计。
2. 把app包梳理，把所有要改动的文件重命名为原名.bak
3. 把要写的文件创建好（名字需讨论）
4. 每个文件只写字段、函数名字和签名和注释，函数内容就写几句注释，大致说他应该干什么，逻辑就直接写pass（字段、函数、签名需讨论）
5. 所有架构搭完后，可以参考对应的bak文件（可能名字要改，但是可以参考），把自己职责相关的内容填进去
6. 先不管下层内容，也不管还没写完代码的报错

## App 包架构梳理

App 的公共流程和 Agent 对接已实现，资源服务及其他业务线按已确定的调用契约接入，下层按照 App 调用实现。职责与契约见 [App 包设计](app-design.md)。

- [x] 按六文件职责组织入口、环境、共享资源、接线、退出和客户端流程。
- [x] 向媒体与模型构造入口交付完整配置，内部初始化由所属服务负责。
- [x] 客户端消费 Platform 的分类结果，不在 App 解析连接地址或重新判断失败性质。
- [x] 退出步骤使用结果枚举和共用批量执行函数，保持统一 30 秒预算与部分失败清理。
- [x] 统一权限管理类型及现行调用方为 `Permission`。
- [x] 测试覆盖资源部分失败、环境参数和退出辅助逻辑；期限测试使用虚拟时间。
- [ ] 实现 Tool、Platform、Background 的目标总管及 Platform.RunClient 结果契约，补齐跨线接线。
- [x] 收窄 Agent 的 Hook 边界：声明 `hooks.Points`，提供 `RegisterHooks`、`StartHookRequest`，业务流程仅调用 Agent 自有扩展点；Hook 管理能力仅用于命令。
- [x] Hook 根包实现 ConnectAgent：注册并持有一组回调，转换业务参数与 Hook Event 并处理结果转换；逐 handler 登记回调只传 context 与名称，不持有 Agent 内部组件。
- [x] Hook 根部 context_to_event.go 负责外部事实转换；pipeline 组织续接、普通规则及错误钩子，构造时注入日志和 runtime 续接能力；注册查询契约与事件辅助逻辑归 share。
- [x] Hook.Start 串行完成初始加载和平台订阅，重复启动等待完成；已补并发启动回归用例。
- [x] Hook 根包持有 PlatformConnected 订阅和逐平台队列，管理全局 Hook 输出与真实退出；全局平台工作不登记 Agent 请求。已补根包桥接与生命周期用例，暂未执行。
- [x] Hook.BeginClose 保留 Agent 收尾能力；Close 解绑并等待已接纳调用和平台工作，再关闭 runtime，Done 等待实际退出。
- [x] Hook 子包接入根包装配的能力：runtime.Route 接收登记回调并返回来源 LogResult；control 候选 pipeline 注入日志、使用空续接；rules/runtime 消费工具、发送、日志、通知及所需媒体能力，rules 使用按平台名称调用的 API；内部不反向导入根包。
- [x] pipeline.Manager 作为具体执行器，将只读策略传给 rules 逐动作校验；share 保留调用快照和纯查询 RuntimeInfo，runtime 保留运行时枚举及私有媒体接口。
- [x] 管理命令使用 share 查询类型；迁移流水线日志与旧装配层覆盖，删除 module／builtin／process 包装。
- [ ] 编译并运行 Hook 及相关管理命令测试，完成整条接线验证；当前仅完成格式整理与静态核对。
- [ ] 实现媒体 NewMediaCenter、模型 NewModelManager 的构造契约。
- [ ] 实现按配置区分的服务独占运行占用。
- [ ] 适配进程入口的新 App API 与 `client` 命名。
- [ ] 下层接口补齐后，验证完整资源构造、App 启停和客户端链路。

退出辅助函数与共享资源关闭测试可通过临时 overlay 隔离缺失的下层调用，使用实际函数，不提供假总管；完整应用暂不能构建运行。App 的 13 个原生产文件和 12 个原测试文件保存在 `*.go.bak` 中，不参与构建。

## 后台工具统一预载

统一入口为 Tool 根部 `Supervisor.PrepareBackground(ctx, share.BackgroundPreloadRequest)`，返回 `share.BackgroundPreload`。请求包含 `SessionID`、`Names`、`AllowedToolNames`、`ELwispTools`；本地名称可指向工具、Skill 或标签，外部声明沿用 `ELwispToolDeclaration`。调用方提交返回的 `Update`，预载不写会话状态。

- [ ] Cron、Elnis 及以后新增的后台任务，全部通过根部 `PrepareBackground` 准备工具；不得直接访问 Tool 内部 Registry、PreloadService 或另建工具获取入口。
- [ ] Cron 提供任务预载名单；Elnis 负责声明授权及适配，为每个外部工具补齐 `CanonicalName`、`EventKey`，将 `ELwispTools` 随任务传递。模型侧名称和事件关联字段属于内部调用信息，不属于外部声明协议字段。
- [ ] Background 统一转交预载请求并提交返回状态；移除 Elnis 预先构造 `DiscoveredTool`、Background 追加或转换工具状态及重复过滤的旧路径。上游只提供工具材料与已裁定的范围，工具准备逻辑统一归 Tool。
- [ ] 新会话统一预载并提交；续跑读取已提交工具状态。后台执行仅使用任务已有工具集合，不回退到全局目录获取未预载工具。
- [ ] 调用方迁移完成后，删除旧 `ELwispInjection` 与绕过统一预载的转换入口，并验证 Cron 本地工具／Skill／标签、Elnis 本地加远程声明、空名单、范围限制及续跑行为。

# 任务清单

- [ ] CLI TUI 支持 Markdown 渲染。
- [ ] CLI 用户名和助手显示名可配置。
- [ ] CLI 支持本地图片输入。
- [ ] 周期 Cron 支持过期时间。
- [ ] 从公共 config 包获取命令前缀，提示消息自动组装。

## 暂缓

- MCP 接入：暂不考虑，使用 Skill。
- 子 Agent：暂不考虑，信息交流会带来噪声或损失。
