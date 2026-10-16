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

- [x] 工具媒体：复用已持有媒体的元数据和导出结果，已完成。
  - MediaCall 按 ID 集中保存元数据、引用及成功导出，OpenHeld 打开已持有的记录；输出导入直接登记持有，Go 参数准备复用完整条目及小文件 base64，保留参数顺序和重复项。
  - 复用限于同次调用；Shell 跨调用检查缓存并刷新时间，Go／TOML 使用调用专属目录。引用持续到 Close，失败结果不缓存，引用释放失败可重试。入口：[internal/tool/media_runtime.go](../internal/tool/media_runtime.go)、[internal/tool/skill/media.go](../internal/tool/skill/media.go)、[internal/media/manager.go](../internal/media/manager.go)。

- [x] 重复预览：复用同次调用在确认前生成的预览。
  - Runner 局部持有 PreparedCall 并传给 worker；文件编辑／撤销、长期记忆和 Skill 修改复用准备快照，保留执行前的 revision、目标及绑定校验；AgentSkill 正文在执行时读取。
  - 文件 context 只保留绑定，准备对象和提交校验显式传递。入口：[internal/fileops/edit_service.go](../internal/fileops/edit_service.go)、[internal/toolrun/runner.go](../internal/toolrun/runner.go)。
  - `internal/tool/prepared.go`、`internal/tool/executor.go`、`internal/toolrun/toolrun.go`、`internal/fileops/binding.go`、`internal/fileops/commit.go`、`internal/fileops/rollback_service.go`、`internal/fileops/atomic_write.go`。
  - `internal/tool/builtin/file_tools.go`、`internal/tool/builtin/file_rollback.go`、`internal/fileops/rollback.go`、`internal/tool/builtin/long_memory.go`、`internal/tool/skill/modify_el_skill.go`、`internal/tool/skill/agent_skill_tool.go`。

- [x] 规则卡状态：ELyph 规则集中在公共包，工具、技能和后台任务共享 Session 首次展示状态；声明与最终组装分离，移除 context 中的 shown／new 集合，状态随消息原子提交。
  - `internal/elyph/rules.go`、`internal/tool/tool.go`、`internal/tool/builder.go`、`internal/tool/detail.go`、`internal/tool/discover.go`。
  - `internal/toolrun/preload.go`、`internal/toolrun/runner.go`、`internal/toolrun/state.go`、`internal/agent/toolrun_adapter.go`、`internal/agent/tool_directive.go`。
  - `internal/tool/skill/descriptor.go`、`internal/tool/skill/creator.go`、`internal/tool/skill/modify_el_skill.go`、`internal/tool/builtin/cron.go`、`internal/tool/builtin/elwisp_creator.go`、`internal/tool/runtimeinfo/runtimeinfo.go`。
  - `internal/background/background.go`、`internal/cron/execution.go`、`internal/elnis/llm.go`、`internal/agent/background.go`、`internal/agent/input.go`、`internal/turn/manager.go`。
  - `internal/agent/dialogue/preparation.go`、`internal/agent/dialogue/model_call.go`、`internal/agent/dialogue/message_store.go`、`internal/storage/dialogue.go`、`internal/storage/sqlite/dialogue_repository.go`、`internal/contextmgr/state.go`。

- [x] 目录 AST：先按扩展名或标准 Shell shebang 筛选，再完整读取候选文件。
  - 无扩展名及其他扩展名脚本通过 shebang 识别；普通文本中的 mksh 不参与识别。命中后从同一读取流继续读取，保留排序、选择、大小警告和缓存失效行为。
  - `internal/tool/builtin/file_tools_directory.go`、`internal/tool/builtin/file_tools_ast.go`、`internal/tool/builtin/file_tools_test.go`。

- [x] 目录 grep：由 rg 统一枚举候选文件，按明确文件列表分批搜索，流式解码输出。
  - 大文件警告与搜索范围一致；完整统计匹配行数，最大堆只保留展示或 index 需要的候选，按完整相对路径及行号排序。空结果、长行、特殊文件名、进程失败及取消均保持明确结果。
  - `internal/tool/builtin/file_tools_directory.go`、`internal/tool/builtin/file_tools_test.go`。

- [x] 长期记忆同步：批量读取正常／异常索引，复用扫描属性及未变化的格式错误；相同异常状态保留原记录。
  - 格式错误分类仅保存在 store 内存中，不增加数据库字段；重新打开后在实际同步时重新校验异常文件。搜索、查看分类触发常规同步，保留 30 秒冷却；修改／删除找不到 ID 时强制同步，失败允许立即重试。
  - 按文件名排序确定重复 ID 归属，先清理失效索引再写入变化记录，处理删除、改名、改 ID、互换 ID、损坏及重复 ID 恢复。入口：[internal/tool/builtin/long_memory.go](../internal/tool/builtin/long_memory.go)。
  - `internal/tool/builtin/long_memory_test.go`。

- [x] 网页提取：合并相同缓存键的在途请求，调用方独立等待、取消和超时；无人等待时取消共享获取，迟到结果不覆盖新请求。
  - 共享获取超时为 15 秒；force_refresh 跳过已有缓存但可加入在途获取，普通调用已有缓存时立即返回。成功更新缓存，失败保留旧缓存且不缓存错误。入口：[internal/tool/builtin/web_extract.go](../internal/tool/builtin/web_extract.go)。
  - `internal/tool/builtin/web_tools_test.go`。

- [x] Skill 输出缓冲：stdout 最多保留 1 MiB，stderr 最多保留 16 KiB，超限后持续消费管道并丢弃多余输出。
  - 进程成功但 stdout 超限时明确报错，不解析残缺 JSON 或导入媒体；失败、取消和超时保留原错误分类及有界诊断。未超限的完整 JSON 仍支持媒体结果，正文沿用 16 KiB 展示上限。入口：[internal/tool/skill/runner.go](../internal/tool/skill/runner.go)。
  - `internal/tool/skill/command_tool.go`、`internal/tool/skill/media.go`、`internal/tool/skill/runner_test.go`、`internal/tool/skill/media_test.go`。

共同要求：

- 复杂批次实施前讨论实现、文件组织和命名，不擅自扩大范围。
- 测试只验证业务行为；清理专门验证跳过读写的测试，不新增 I/O 次数断言。
- 不修改用户文档和 CHANGELOG；内部职责变化按需同步 devdocs。
- 修改 Go 文件后运行 gofmt 和相关包测试；涉及并发、引用或取消时补充对应的 race 检查。

## 待确认与暂缓

- MCP 接入：暂不考虑，建议使用SKill
- 子 Agent：暂不考虑，信息交流必然伴随噪声或者损失
