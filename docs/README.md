# ElBot 文档

这里是面向用户的 ElBot 文档。开发计划、任务拆分和内部设计资料已移到 [`../devdocs/`](../devdocs/)。

## 推荐阅读顺序

1. [快速开始](getting-started.md)：从配置 API Key 到启动 CLI，完成第一次对话。
2. [配置说明](configuration.md)：了解配置文件、路径规则、Provider、运行数据和插件目录。
3. [命令速查](commands.md)：查看常用 slash 命令和会话管理方式。
4. [核心概念](concepts.md)：理解 Chat / Work 模式、工具发现、Session、Hook、Cron、Skill 和安全策略。
5. [Hook](hooks.md)：规则 Hook 配置、action 类型、segments 多段输出、exec 脚本和表情提取示例。
6. [Elnis 监听枢纽](elnis.md)：了解 Elnis、Elwisp、Elvena 和外部事件接入。
7. [Elnis 配置与使用](elnis-usage.md)：启用 Elnis、配置 Elwisp，并用 Elvena 投递事件。

## 文档维护约定

- `README` 只保留项目介绍、快速入口和最小启动路径。
- `docs/` 放用户使用文档，避免混入开发任务流水账。
- `devdocs/` 放开发计划、任务拆分、架构草案和接口草案。
- 新增用户可见功能时，优先更新对应专题文档，而不是把细节全部塞进 README。
- `CHANGELOG.md` 为中文源文件，`CHANGELOG.en.md` 由 GitHub Actions 自动翻译，请勿手改；以版本（tag）为节点，平时改动写进 `## [Unreleased]`，发版时将其改为 `## [vX.Y.Z] - YYYY-MM-DD` 并新建空 Unreleased。

## 当前状态

ElBot 仍在开发中，配置、命令和扩展接口可能调整。文档优先覆盖当前稳定且常用的使用路径；实验性能力会尽量标注。
