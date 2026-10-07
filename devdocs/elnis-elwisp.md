# Elnis / Elwisp 监听架构

<!-- locator:elnis -->

Elnis 是 ElBot 内部事件入口，Elvena 定义公共协议，Elwisp 提供外部事件和工具。Elnis 负责接收、鉴权、规范化、去重和分发，最终执行与投递由宿主裁决；不管理 Elwisp 生命周期，也不实现聊天平台语义。

配置、协议字段与示例见 [Elnis 使用文档](../docs/elnis-usage.md)，模块入口见 [代码地图](code-map.md#elnis--elvena--elwisp)。

## 接收与分发

`Elvena HTTP → 鉴权 → 校验／规范化 → 授权 → 持久化去重 → record / direct / llm`。

- Runtime 接收 `POST /elvena/v2/events` 和 `POST /elvena/v3/events`，提供 `/healthz`；限制 body 大小并要求单个 JSON object。
- 使用 Bearer 或 X-Elnis-Token 鉴权。token 名只用于来源审计，Elwisp 身份来自 elwisp.name；日志不记录 token 原文。
- 事件唯一键为 `elwisp.name + source + id`。重复键不再次分发；相同键但 hash 不同仅记录告警，不覆盖已保存事件。
- record 只存记录；direct 在当前请求中发送文本／segments 并执行 raw／capability calls，calls-only 不额外发消息；llm 落库后入队，HTTP 不等待模型完成。
- llm 队列在内存；应用在 worker 启动前调用 Elnis 的恢复入口，由 Elnis 决定将未持久化报告的 accepted／queued／running 事件标记失败，事件仓储原子更新状态并释放引用。即使 Elnis 禁用也执行；已持久化的报告 outbox 单独恢复。中断执行可能已产生工具副作用，因此不自动重放。

## 来源、工具与投递裁决

- 未配置的 Elwisp 默认启用；显式 enabled=false 拒绝。单 Elwisp 可限制 token、覆盖内部工具白名单并禁用外部工具或目标。
- 内部工具使用全局 allowed_tools，单 Elwisp 配置可覆盖；工具／Skill／tag 通过共享 PreloadService 展开，根工具在执行前仍校验权限。
- 外部工具使用 `elwisp.<name>.<tool>` 命名空间，由 payload 显式注入对应 Session，不通过 discover_tool 发现。ToolRun 统一组织当前工具视图、路由、记录和结果回灌。
- native 工具执行宿主权限与风险策略；外部 Elwisp 工具按无人值守 low 风险处理，影响由远端环境负责。工具结果返回执行层，平台输出走共享发送服务。
- 投递目标由请求 targets、全局／单 Elwisp disabled targets 和启用平台共同确定。platform-only 的请求目标指向平台管理员，platform-only 的禁用项禁止整个平台。
- 事件持久化请求目标与解析目标，实际回执使用平台返回的 scope／message ID，不根据请求自行拼关联。

## 后台 LLM

Elnis 与 Cron 复用 `internal/background` 类型、JSON 结果解析和 Agent.RunBackground；Elnis 管事件与投递，Cron 管调度。

- Session 固定 background，模型取 elwisp1/2/3 槽位，未指定或未配置回退 work。身份、sandbox 和首轮工具由后台准备层确定。
- native 工具在 `elnis/<elwisp>` sandbox 执行；后台不读取 AGENTS.md，不开放 workspace／discover_tool。Soul、记忆、Skill 和显式 tag 提示复用公共 Prompt 来源。
- 首轮将工具／Skill／外部工具状态一起提交；同 Session 续跑和 JSON 修正复用首轮状态，不扩大工具集合。失效远端工具保留历史 schema，调用返回不可用结果。
- 最终结果解析 completed、need_report 和报告输出。无需报告直接完成；有报告内容和目标时，先保存结果与 outbox，再立即尝试投递。
- 前台接管后等待真实 Execution 结果并记录接管，停止后台 JSON 修正、自动报告和未开始补投递；复用该 Session 不恢复后台身份。

## 报告 outbox

`queued → running → result_ready → delivering → completed`；执行／入队失败写 failed，无需报告跳过投递阶段。

- `elnis_events` 保存事件身份、内容 hash、目标、工具声明、状态、Session、结果与错误；`elnis_report_deliveries` 按 event_id／ordinal 保存有序投递项。
- 结果和逐目标、逐 output 项同事务保存；报告保存后进入 result_ready，投递前 claim 为 delivering。
- 按目标合并连续未完成项发送，先用实际回执建立消息关联，发送成功后逐项保存回执并标记 delivered。部分失败保留成功关联，该批未完成项仍待重试，可能重复发送；所有项 delivered 后事件才 completed。
- Runtime 定时重试未完成项，启动将中断的 delivering 恢复为 result_ready。发送成功而 receipt 未落库时可能重复发送，语义为至少一次。
- 事件终态和引用释放使用独立于调用取消的有界清理 context，同事务完成；不能因请求取消遗留持有引用的失败事件。

## 媒体与 workspace

- image／file 来源接受 HTTP(S)、data URI 和完整 media ID；不接受外部本地路径，也不开放宿主媒体管理 RPC。
- record、重复、拒绝和仅排队的 URL 不下载。排队的已有媒体 ID 建立事件引用，LLM 执行时物化；direct 有实际目标才导入或复用中心。
- workspace 普通文件按文件维护规则保留。宿主导入／导出通过 os.Root 校验，报告附件在准备 outbox 时导入，投递项只保存稳定 ID。
- 发送、重试和已发送缓存各自持有引用；有序媒体关联由通用发送边界建立。缓存用 retention_days，非正值不缓存。
- 引用清理、孤儿宽限期和后端删除复用[媒体中心约束](architecture.md#媒体引用与清理)。Elwisp 远端工具只操作远端文件，不承担宿主媒体传输。

## Runtime 生命周期与审计

Elnis 由 app 装配，与平台、Cron runtime 共享应用 context。退出停止接收并取消／等待 worker；持久化报告与投递位置留给启动恢复。

日志和审计携带来源标签、Elwisp、source、event ID、mode、状态及 Session／目标，覆盖接收、拒绝、去重、执行和投递失败；来源身份不靠全局当前会话补全。
