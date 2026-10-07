# 任务清单

## 全局信号与日志统一改造

详细设计见 [全局信号与日志中心设计](global-signals.md)。阶段 1、2 已完成，查询与命令改造留在阶段 3；本轮只实现日志所需的全局事件。阶段按顺序推进，每阶段保持可编译、相关测试通过，再更新完成状态。

| 阶段 | 工作内容 | 验收条件 |
| --- | --- | --- |
| 1 | 建立全局信号与日志中心 | 全局发射落盘；过滤、脱敏限长、快照、独立故障报告及生命周期测试通过 |
| 2 | 迁移全部日志生产者 | 移除 App 日志转换和 Logger／Audit 旁路；协议依赖正确；Hook 不重复、重试不遗漏 |
| 3 | 查询、命令与整体收尾 | 反向查询和命令行为正确；全量测试、相关 race 测试及依赖检查通过，文档与实际行为一致 |

- [x] 阶段 1：建立全局信号与日志中心。
- [x] 阶段 2：迁移全部日志生产者。
- [ ] 阶段 3：查询、命令与整体收尾。

设计文档作为实施基线，不单独划分阶段；架构和代码地图随实际代码改造更新。阶段 1 验证记录：

- 实施前 `go test ./internal/signal ./internal/logging ./internal/contextinfo ./internal/app` 通过。
- 实施后 gofmt、`go test ./...` 和 `go test -race ./internal/signal ./internal/events ./internal/logging ./internal/app` 通过。
- 全局发布覆盖真实文件与 Reader、字段快照、独立等级过滤、脱敏限长、重复中心与失败清理、写入失败 stderr、背压及退出预算。旧 Logger 共用文件写入器，集中内容处理仅覆盖新全局链路。

阶段 2 验证记录：

- 全部业务日志通过 `events.EmitLog` 发布，Agent／Session／Model Manager 自行维护同步日志投影；已删除 App 日志转换文件和旧 Logger／Audit 注入入口。
- 先复现没有通知订阅者时重试日志缺失，再验证来源投影修复；覆盖原始 Hook 失败只记录一次、通知保留、通用上游诊断、来源时间与身份、关闭断开订阅。
- 真实生产装配落盘验证 INFO／DEBUG 正文规则、WARN／ERROR 下保留用量审计、service 告警和启动记录；依赖边界测试防止业务直接使用 Logger、绕过发布快照或依赖具体协议错误。
- gofmt、`go test ./...` 通过；signal、events、logging、agent、session、modelmgr、hook、notification、app、cron、elnis、llm、platform、delivery 相关包及其子包的 race 测试通过。

保留现有共同的 30 秒退出预算、同步业务持久化和日志轮转配置。其他领域的全局信号按需另行确认；本任务不修改远程部署，不自动提交。

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
