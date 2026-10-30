# ElBot 文档

面向用户的文档。开发计划、架构和内部设计在 [`../devdocs/`](../devdocs/)。

## 阅读顺序

1. [快速开始](getting-started.md)：配置 API Key → 启动 CLI → 第一次对话。
2. [配置说明](configuration.md)：配置文件布局、路径规则、Provider、运行数据、插件目录。
3. [命令速查](commands.md)：slash 命令与会话管理。
4. [核心概念](concepts.md)：Chat / Work 模式、工具发现、Session、Hook、Cron、Skill、安全策略。
5. [Hook](hooks.md)：规则 Hook、action 类型、多段输出、exec 脚本。
6. [Elnis 监听枢纽](elnis.md)：Elnis / Elwisp / Elvena 与外部事件接入。
7. [Elnis 配置与使用](elnis-usage.md)：启用 Elnis、配置 Elwisp、投递事件。
8. [ELyph 任务表示法](elyph.md)：Skill 和 Cron 任务条共用的任务语法。
9. [前端 API](frontend-api.md)：WebSocket 协议、消息类型和补全接口。

## 文档维护

- 维护规则见 [`../AGENTS.md`](../AGENTS.md)，本目录只放用户使用文档，内部资料放 [`../devdocs/`](../devdocs/)。
- `CHANGELOG.md` 为中文源文件，`CHANGELOG.en.md` 由 GitHub Actions 自动翻译，请勿手改。以版本（tag）为节点：平时改动写进 `## [Unreleased]`，发版时改为 `## [vX.Y.Z] - YYYY-MM-DD` 并新建空 Unreleased。

