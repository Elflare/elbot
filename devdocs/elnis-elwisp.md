# Elnis / Elwisp 监听架构

<!-- locator:elnis -->

Elnis 接收并分发事件，Elvena 定义协议，Elwisp 提供外部事件和工具。宿主裁决授权、执行与投递，不管理外部监听器生命周期。

配置和协议示例见[使用文档](../docs/elnis-usage.md)，文件入口见[代码地图](code-map.md#elnis--elvena--elwisp)。

## 接收与分发

`HTTP → 鉴权 → 规范化与授权 → 持久化去重 → record / direct / llm`。

- Runtime 接收 POST /elvena/v2/events 和 /elvena/v3/events，提供 /healthz；限制 body 大小，仅接受单个 JSON object。
- Bearer 或 X-Elnis-Token 鉴权；token 名用于审计，Elwisp 身份取 elwisp.name，日志不记录 token 原文。
- 唯一键为 elwisp.name + source + id；重复不分发，同键不同 hash 只告警，不覆盖原事件。
- record 只记录；direct 在当前请求内发送文本／segments 并执行 raw／capability calls，calls-only 不额外发消息；llm 落库后入内存队列，HTTP 不等待模型完成。
- worker 启动前恢复 accepted／queued／running 中未持久化报告的事件，将其标记失败并原子释放引用，即使 Elnis 禁用也执行；工具可能已有副作用，因此不重放。已有报告按 outbox 恢复投递。

## 来源、工具与投递裁决

- 未配置的 Elwisp 默认启用，显式 enabled=false 拒绝；单 Elwisp 可限制 token、覆盖内部工具白名单、禁用外部工具或目标。
- 内部工具按全局 allowed_tools 或单 Elwisp 覆盖值，经 PreloadService 展开工具／Skill／tag，执行前仍校验根工具权限。
- 外部工具用 `elwisp.<name>.<tool>` 命名空间，随 payload 显式注入 Session，不经 discover_tool 发现；统一走 ToolRun 路由、记录与结果回灌。
- native 工具执行宿主权限和风险策略；远端 Elwisp 工具按无人值守 low 风险执行，其副作用属于远端环境。
- targets、全局／单 Elwisp 禁用目标和启用平台共同裁决投递范围；platform-only 请求指向平台管理员，platform-only 禁用项屏蔽整个平台。
- 事件保存请求与解析后的目标，关联使用平台实际返回的 scope／message ID。

## 后台 LLM

Elnis 通过 background.Runner 接口调用公共后台执行，由 Dialogue 的 BackgroundRunner 准备身份、Session、sandbox 与首轮工具；JSON 结果解析与 Cron 共用。

- Session 为 background，模型使用 elwisp1/2/3，未指定或未配置回退 work；native 工具在 `elnis/<elwisp>` sandbox 执行。
- 后台不读取工作目录中的 AGENTS.md，不开放 workspace／discover_tool；Prompt、工具权限与规则卡遵循[公共工具约束](architecture.md#tool-runtime工具状态与文件)。
- 首轮提交工具／Skill／外部工具状态，续跑与 JSON 修正复用该状态；失效远端工具保留历史 schema，调用返回不可用。
- 结果解析 completed、need_report 和报告内容；有内容与目标时先保存结果和 outbox，再立即投递，无需报告则完成。
- 前台接管后等待真实 Execution 结果，停止后台修正、自动报告及未开始补投递；复用 Session 不恢复后台身份。

## 报告 outbox

`queued → running → result_ready → delivering → completed`；入队／执行失败为 failed，无需报告时跳过投递。

- elnis_events 保存事件、目标、工具、状态、Session、结果与错误；elnis_report_deliveries 按 event_id／ordinal 保存有序投递项。
- 结果和逐目标／output 项同事务保存，进入 result_ready，投递前 claim 为 delivering。
- 同目标连续未完成项合并发送，部分成功保留关联，其余待重试；全部 delivered 才 completed。
- Runtime 定时重试，启动将中断 delivering 恢复为 result_ready；发送成功但回执未落库可能重复，采用至少一次投递。
- 终态与引用释放使用独立于调用取消的有界 context，在同事务完成。

## 媒体与 workspace

- image／file 接受 HTTP(S)、data URI 和完整媒体 ID，不接受外部本地路径或宿主媒体 RPC。
- record、重复、拒绝和排队 URL 不下载；排队已有 ID 建事件引用，LLM 执行时物化，direct 有实际目标才导入。
- workspace 按普通文件规则维护，导入／导出经 os.Root 校验；outbox 附件在准备时导入，只保存稳定 ID，远端工具只操作远端文件。
- 引用、输出关联和清理遵循[媒体中心约束](architecture.md#媒体引用与清理)；已发送缓存使用 retention_days，非正值不缓存。

## Runtime 生命周期与审计

Background Module 装配恢复、服务与 HTTP workers；runtime 共享应用 context，退出停止接收并取消、等待 worker，持久化报告留待启动恢复。

审计覆盖接收、拒绝、去重、执行和投递，JSON 正文保存 Elwisp、source、event ID、mode、目标等查询事实，Session 关联写入 Identity；日志遵循[公共日志契约](architecture.md#信号与订阅)。
