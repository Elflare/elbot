# 任务清单

- [ ] CLI TUI 支持 Markdown 渲染。
- [ ] CLI 用户名和助手显示名可配置。
- [ ] CLI 支持本地图片输入。
- [ ] 周期 Cron 支持过期时间。
- [ ] 从公共config包获取命令前缀，所有提示消息自动组装

## 工具代码与 I/O 清理

先合并处理第一批简单项，后续复杂项按条目分别讨论实现和命名、分别修改。

- [x] 第一批：简单项合并处理，已完成。
  - 工具发现状态：合并后的 metadata 未变时保留原 UpdatedAt，使 Session 仓储可以跳过同值保存。入口：[internal/toolrun/state.go](../internal/toolrun/state.go)。
  - 常驻记忆：在现有锁内比较修改前后内容，跳过同值保存，保留内容校验及空记录删除逻辑。入口：[internal/memory/resident/store.go](../internal/memory/resident/store.go)。
  - 看图：已有媒体 ID 直接进入图片校验，消除此前单独读取元数据的步骤；URL 和文件导入后仍校验图片。入口：[internal/tool/builtin/view_image.go](../internal/tool/builtin/view_image.go)。
  - 无用代码：清理旧环境读取链及启动层 WithConfigEnvDir 注入、无调用方的 goSkillConfigDir；删除只赋值的 directorySearchMatch.Text／Language 和 preparedFile.Size；生产代码中仅供测试调用的 resultFromStdout 包装移入测试代码。
    - `internal/app/foundation.go`、`internal/tool/skill/go_source.go`、`internal/tool/builtin/file_tools_directory.go`、`internal/tool/builtin/file_manager.go`、`internal/tool/skill/runner.go`。
  - Skill 执行：错误输出摘要只在失败分支计算，成功路径直接处理结果。入口：[internal/tool/skill/runner.go](../internal/tool/skill/runner.go)。
  - 不新增生产文件、函数或公共接口；比较逻辑放在现有函数内，新增局部变量已确认使用 metadata、before、exists，其余命名沿用现有。

- [ ] 工具媒体：复用已持有媒体的元数据和导出结果。
  - 已持有媒体在导出时仍经 Open 重读元数据并建立额外临时持有；重复媒体参数还会重复复制文件，小文件 base64 准备也会重复读取。
  - 单独明确同次调用内的复用范围、工作目录、引用生命周期及失败清理。入口：[internal/tool/media_runtime.go](../internal/tool/media_runtime.go)、[internal/tool/skill/media.go](../internal/tool/skill/media.go)、[internal/media/manager.go](../internal/media/manager.go)。

- [ ] 重复预览：复用同次调用在确认前生成的预览。
  - 文件编辑／撤销、长期记忆和 Skill 管理工具在预检及确认详情阶段重复读取、计算预览；edit_file 虽保存预检结果，仍先重新读文件和计算 diff。
  - 保留执行前的 revision、目标文件及绑定有效性校验。入口：[internal/fileops/edit_service.go](../internal/fileops/edit_service.go)、[internal/toolrun/runner.go](../internal/toolrun/runner.go)。
  - `internal/tool/builtin/file_tools.go`、`internal/tool/builtin/file_rollback.go`、`internal/fileops/rollback.go`、`internal/tool/builtin/long_memory.go`、`internal/tool/skill/modify_el_skill.go`、`internal/tool/skill/agent_skill_tool.go`。

- [ ] 目录 AST：先筛选语言，再完整读取候选文件。
  - 当前先完整读取所有大小合格的文件，再判断是否支持语言，导致无关文件被读取后丢弃。
  - 使用扩展名及少量文件头筛选，保留无扩展名 Shell、现有排序、选择和缓存失效行为。入口：[internal/tool/builtin/file_tools_directory.go](../internal/tool/builtin/file_tools_directory.go)、[internal/tool/builtin/file_tools_ast.go](../internal/tool/builtin/file_tools_ast.go)。

- [ ] 目录 grep：减少重复目录遍历和输出缓存。
  - 当前为大文件警告先遍历目录并取文件属性，随后 rg 再遍历搜索；匹配输出全部收集后才截取展示。
  - 合并文件枚举并流式处理结果，保持匹配总数、排序、index 选择和大文件提示语义。入口：[internal/tool/builtin/file_tools_directory.go](../internal/tool/builtin/file_tools_directory.go)。

- [ ] 长期记忆同步：批量读取并复用索引状态。
  - 当前逐文件查询索引状态，末尾又查询完整路径列表；未变化的损坏文件仍重复读取、解析并保存错误记录。
  - 批量读取正常／异常索引，复用未变化的格式错误状态；正确处理文件删除、路径及 ID 变化，重复 ID 等依赖全库状态的错误仍需重判。入口：[internal/tool/builtin/long_memory.go](../internal/tool/builtin/long_memory.go)。

- [ ] 网页提取：合并相同缓存键的在途请求。
  - 当前相同键并发未命中缓存时，各调用分别发起 HTTP 请求。
  - 实施前明确等待方取消、请求失败及 force_refresh 的行为。入口：[internal/tool/builtin/web_extract.go](../internal/tool/builtin/web_extract.go)。

- [ ] Skill 输出缓冲：分别处理诊断输出与完整 JSON 结果。
  - 当前子进程 stdout／stderr 使用无限增长的缓冲区，进程结束后才截断展示。
  - 实施前分别确定诊断输出及结构化结果的大小限制和超限处理，保持管道排空及媒体结果解析正确。入口：[internal/tool/skill/runner.go](../internal/tool/skill/runner.go)。
  - `internal/tool/skill/command_tool.go`、`internal/tool/skill/media.go`。

共同要求：

- 复杂批次实施前讨论实现、文件组织和命名，不擅自扩大范围。
- 测试只验证业务行为；清理专门验证跳过读写的测试，不新增 I/O 次数断言。
- 不修改用户文档和 CHANGELOG；内部职责变化按需同步 devdocs。
- 修改 Go 文件后运行 gofmt 和相关包测试；涉及并发、引用或取消时补充对应的 race 检查。

## 待确认与暂缓

- MCP 接入：暂不考虑，建议使用SKill
- 子 Agent：暂不考虑，信息交流必然伴随噪声或者损失
