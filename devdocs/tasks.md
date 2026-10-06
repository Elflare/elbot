# 任务清单

本文只跟踪有效待办。后续设计见 [core-refactor.md](core-refactor.md)，当前实现见 [architecture.md](architecture.md) 和 [code-map.md](code-map.md)。

基础工程、会话／命令／工具／平台能力、Elnis 及核心重构阶段 1–15 已完成；16.1 公共单轮与 Chat 路线、16.2 协议客户端、16.2a 公共事实与 provider 绑定已验收，全量测试、race 及启动／关闭回归通过。后续任务经实际接入与验证后勾选，出现新的实现选择或歧义先讨论。

<!-- locator:core-refactor -->
<a id="core-refactor"></a>
## 核心重构：阶段 16

### 16.2a：公共信息与 provider 能力绑定

#### 公共信息迁移

- [x] 将 chatinfo 改造为 contextinfo，统一四组公共事实的定义与存取，不保留并行副本；按 Conversation／Actor／Execution／Model 提供阶段性快照及明确的缺失状态。
- [x] 接入平台信息、security 身份、Session／Request／执行关联及固定模型选择，保留原连接回复与前台接管刷新边界。

#### provider 能力绑定

- [x] 将 Route 的 Protocol 登记／查询改为 provider 绑定，保留客户端与业务装配分工、校验和封闭机制、独立文本能力及主对话缺失能力的准入拒绝。
- [x] 移除 Selection.Protocol 和 Client.Protocol()；公共 Model 事实来自选择与绑定描述，持久化归属独立保留。

#### 消费者与事件接入

- [x] dialogue／contextmgr 按目标 provider／源会话归属取得能力，Prompt、Hook 和展示读取对应公共事实。
- [x] 衔接已有类型化信号与 app 订阅管理，保持事件快照、实际调用 context、背压及关闭策略。
- [x] 保留权限、活动状态、取消与原生材料边界；独立文本不增加公共协议字段，摘要及 Usage 同步返回。

#### 验证与文档同步

- [x] 覆盖多 provider、不同路线、缺失／错误绑定、信息阶段可用性、固定快照、接管及原连接回复，保留事件与生命周期回归。
- [x] 验证协议分工、依赖方向及同步提交边界，同步当前架构和代码地图。

### 16.3：Responses loop 与原生持久化

- [x] 接入独立准备、原生模型调用、loop、上下文与工具结果组织，复用 ElBot ToolRun。
- [x] 分别保存原生记录与展示消息，建立 checkpoint 关联；共享提交口同步保存业务消息、媒体引用与原生状态，复核 binding／attempt 和旧 checkpoint，拒绝迟到覆盖。
- [x] 接入服务端续链并保留原生 items；Responses 返回调用集合、ID、名称和参数对 Hook 只读，Chat 保留参数改写，调用头及结果逐个同步提交。
- [x] 覆盖多轮工具、pending、停止、确认、媒体、结构化输出、用量、接管、固定快照和部分成功；全量测试、受影响包 race、go vet 及文档检查通过。

### 16.4：兼容检查、恢复、压缩与 fork

- [ ] 接入共享兼容规则：Chat 可跨厂商，同厂商 Responses 可切模型，涉及 Responses 的跨厂商切换拒绝。
- [ ] 处理全局默认、后台覆盖和前台接管；兼容检查先于压缩与模型调用。
- [ ] 链失效时凭同厂商完整原生材料恢复；缺失时拒绝，不重跑工具或退成文字历史。
- [ ] 公共压缩分派接入 Chat 摘要和 Responses 原生窗口，复用新 Session 创建与交接。
- [ ] Responses fork 使用指定业务回复的完整 checkpoint，继承归属，拒绝不完整位置。

### 16.5：整体验收

- [ ] Review 职责、依赖、接口与状态边界。
- [ ] 覆盖两路线的实际装配、切换、恢复、压缩、fork、接管与回复提交。
- [ ] 验证测试协议可经登记接入，共享层无协议专用分支、反向依赖、万能容器或重复执行状态。
- [ ] 全量测试、受影响包 race 和启动／关闭回归通过，同步实际文档；用户行为变化按仓库规则更新说明。

## 其他待办

- [ ] CLI TUI 支持 Markdown 渲染。
- [ ] CLI 用户名和助手显示名可配置。
- [ ] CLI 支持本地图片输入。
- [ ] 周期 Cron 支持过期时间。
- [ ] 从公共config包获取命令前缀，所有提示消息自动组装
- [ ] 看图工具

## 待确认与暂缓

- [ ] 临时hook或者一次性hook
- MCP 接入：暂不考虑，建议使用SKill
- 子 Agent：暂不考虑，信息交流必然伴随噪声或者损失
