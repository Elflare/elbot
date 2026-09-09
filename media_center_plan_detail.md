
# 统一 Media Center 与多媒体引用链路 Details


## Details

**Summary**

在 ElBot 核心层引入统一 Media Center，负责媒体资源的导入、读取、存储后端、LLM 解析、引用关系和清理。平台、Hook、Elnis、Skill、Tool 不再各自维护媒体传输逻辑，而是通过统一 API 使用媒体。

核心原则：

```text
Chat History
    保存平台原始消息和原始媒体引用，不负责物化媒体

Agent Core
    只有真正进入 ElBot 处理流程时，才物化需要使用的媒体

Session / Tool Transcript
    保存稳定的 media:<sha256> 引用

Media Center
    管理媒体本体、存储后端、引用关系和生命周期

LLM Resolver
    根据本次请求全部媒体大小和 file_delivery 配置，
    在 data URL 与 S3/R2 presigned URL 之间选择

Elnis Workspace
    继续管理 Elnis 自己的工作文件，
    仅在需要跨边界时通过 Media API 导入或导出
```

媒体 ID 固定为：

```text
media:<sha256>
```

`sha256` 同时承担内容寻址、去重和媒体身份职责，不额外增加随机 ID 或 SHA-256 映射字段。

**Key Changes**

配置方面：

- 不新增配置项。
- 复用现有 `[file_delivery]` 配置。
- `max_direct_base64_bytes` 表示一次 LLM 请求中所有媒体原始二进制大小的总和。
- 不按单张图片单独判断是否走 base64。
- 请求总大小不超过阈值时，所有媒体统一走 data URL。
- 请求总大小超过阈值时，所有媒体统一走 S3/R2。
- `backend = "base64"` 超过阈值时返回明确错误或进入已有文本回退。
- `backend = "s3"` 统一走 S3/R2。
- `backend = "hybrid"` 按总大小在 base64 和 S3/R2 之间整体切换。
- S3/R2 预签名 URL 使用代码内固定的临时有效期，不新增 TTL 配置；默认按约一小时处理。
- 不公开 Bucket，不持久化预签名 URL。

核心代码：

- 新增 `internal/media` 媒体中心。
- 支持：
  - 从 URL 导入媒体。
  - 从本地文件导入媒体。
  - 从内存字节导入媒体。
  - SHA-256 内容去重。
  - 生成 `media:<sha256>`。
  - 读取媒体元数据和内容。
  - 导出到受控临时目录。
  - 按 LLM 请求能力解析为 data URL 或 S3/R2 URL。
  - 按平台输出能力解析为现有 `delivery.Source`。
  - 添加、删除和查询通用媒体引用。
  - 媒体清理和孤儿资源处理。
- 本地后端和 S3/R2 后端都复用同一个 Media Center 接口。
- 不把本地绝对路径、完整 base64 或预签名 URL 保存进 Session。

Storage：

- 扩展 `storage` 接口，增加媒体元数据和通用媒体引用能力。
- 增加 `media` 表。
- 增加通用 `media_references` 表。
- 引用表不限定于 Session Message，支持：
  - `message`
  - `chat_history`
  - `tool_result`
  - `hook`
  - `skill`
  - `elnnis_event`
  - `output`
  - 其他未来 owner 类型
- 引用记录至少包含：
  - `media_id`（SQLite 列名，Go 内部字段为 `MediaID`）
  - `owner_type`
  - `owner_id`
  - `purpose`
  - 可选的 `session_id`
  - 创建时间
  - 生命周期相关字段
- 使用通用复合唯一约束，避免同一 owner 重复引用同一媒体。
- Message 与引用关系尽量通过 Storage 层事务性写入。
- 删除 Session、Message 或其他 owner 时同步删除对应引用。
- 媒体本体不因单次 owner 删除立即删除，由维护任务统一判断。
- Chat History 增加轻量 `segments` 字段，保存平台原始媒体 URL、平台 file ID、名称、MIME 和大小。
- Chat History 不保存二进制、不保存完整 base64、不保存 Media Center 本地路径。
- 不迁移旧 Session；旧 URL、路径和旧多模态内容不批量下载、不创建 Media Center 资源，旧 fork 和旧 Cron 不做媒体引用回填。

LLM：

- 扩展 LLM `MessageSegment`，增加 `MediaID`。
- 显式媒体参数及尚未发布的 JSON 统一使用 `media`，包括 Hook `media.*` 请求/结果、Skill、Session、Tool Transcript、报告和 Cron；序列化、反序列化、引用提取及 SQL JSON 路径同步调整，不保留 `media_id` JSON 兼容分支。Go 内部统一保留 `MediaID`，SQLite 外键列保留 `media_id`；Elwisp/Hook segment 继续复用原有来源字段。
- Session 和 Tool Transcript 中的新媒体段保存 `media`。
- LLM 请求构造前解析 `media`。
- OpenAI-compatible 请求：
  - 小请求将本地媒体转为 data URL。
  - 大请求上传或复用 S3/R2 对象，并生成临时 presigned GET URL。
- 临时签名 URL只存在于本次请求或短期缓存中，不写入 Session。
- 图片文本投影显示媒体 ID，例如：
  - `[图片 1；名称：cat.png；媒体 ID：media:abc...]`
- 视觉模型不可用时继续使用文本回退。
- 媒体不可用时明确标记“媒体不可用”，不让模型误以为已经看到了图片。
- 旧的 HTTP URL、data URL 读取路径继续兼容，但不参与新 Session 的稳定媒体保存。

Tool / `send_file`：

- 保持 `send_file` 现有 `source` 行为：
  - 本地路径
  - `file://`
  - HTTP(S) URL
- 额外增加 `media` 参数。
- `source` 与 `media` 二选一。
- `media` 通过 Media Center 解析，再交给现有 delivery 和平台发送逻辑。
- 不重做现有平台发送协议。
- 尽量把现有 `FileManager` 的大小判断、base64 和 S3/R2 逻辑下沉到 Media Center，避免复制实现。

Hook 协议：

- Hook 的 `message.segments` 支持通过现有媒体来源字段承载完整 `media:<sha256>`；不新增独立 `media` 字段，也不升级 hook.v2 版本。
- Hook 现有 `url`、`path`、`base64` 行为继续兼容。
- Hook 输入媒体进入 Agent canonical message 时统一物化为 Media Center 资源。
- Hook 输出媒体在现有来源字段中填写完整 `media:<sha256>`，但不强制写入 Session。
- 提供宿主内 Go API：
  - `ImportURL`
  - `ImportFile`
  - `ImportBytes`
  - `Open`
  - `Read`
  - `Export`
  - `ResolveForLLM`
  - `ResolveForOutput`
  - `AddReference`
  - `RemoveReference`
- 扩展 Hook runtime 的协议方法，提供媒体 API：
  - `media.import`
  - `media.read`
  - `media.export`
  - `media.metadata`
- 第一版不开放任意 `media.delete`，删除由引用释放和维护任务负责。
- 小媒体可以通过协议返回 base64。
- 大媒体通过受控临时文件导出，避免 Hook JSON 帧膨胀。
- 不让外部 Hook 直接访问 SQLite、媒体根目录或 S3 凭据。

Elnis：

- 不把 Elnis workspace 中的所有文件纳入 Media Center。
- 保留现有 Elnis workspace 和 Sandbox Cleanup。
- 区分：
  - Elnis 临时工作文件：继续由 workspace 管理。
  - 需要交给 LLM 的输入媒体：按需导入 Media Center。
  - 需要进入 Session、Tool Transcript 或平台输出的文件：按需导入 Media Center。
- 提供 Elnis 使用的媒体 API：
  - 从 Media Center 读取元数据或内容。
  - 将媒体导出到 Elnis workspace。
  - 将 workspace 文件导入 Media Center。
- `downloadSegments` 不再默认把所有 Elnis 文件交给 Media Center。
- Elnis 输入 segment 只有在需要跨越 Elnis 工作边界时才物化为稳定媒体引用，内部 JSON 使用 `media`。
- `report_segments` 继续支持当前 workspace 相对路径语义。
- 报告图片只有在需要后续 LLM 或长期保存时才导入 Media Center。

Skill：

- Skill 可以通过宿主媒体 API 获取、读取、导出和导入媒体。
- 外部 Go Skill 不直接接触 Media Center 文件目录。
- Skill 输出本地文件时，宿主负责校验路径并导入 Media Center。
- Skill 输出媒体段支持：
  - `media`
  - 受控 workspace 相对路径
- Skill 读取媒体时：
  - 小文件可以通过 API 返回 base64。
  - 大文件导出到受控临时路径。
- Skill 结果中的媒体进入 Tool Transcript 或 Session 时建立通用引用。

补充：上述“宿主媒体 API”按 Skill 运行形态通过适配层提供，不表示所有 Skill 直接获得 Media Center 对象。普通 Agent Skill 通过 prompt 传递 `media:<sha256>`；bash 型 Skill 通过 shell Tool Runtime 的 `media_inputs` 显式使用媒体；TOML 配置型工具由 Tool Runtime 处理显式媒体参数；Go Skill 由 `go_skill_run` 完成一次性媒体桥接。当前不实现全局 HTTP/RPC 媒体服务。

平台：

- 最后处理平台适配，不先把 QQ、Telegram 逻辑与核心 Media Center 绑定。
- 各平台只负责提供统一的原始媒体来源描述：
  - 平台名称
  - URL
  - 平台 file ID
  - 文件名
  - MIME
  - 大小
- 平台收到消息时：
  - 实时写入 Chat History。
  - 保存原始平台媒体引用。
  - 不无脑下载图片。
- 只有真正继续进入 ElBot 处理流程时，才调用 Media Center 物化。
- QQ OneBot：
  - 去掉图片在非必要路径中的立即下载。
  - 保留 file ID 和 URL。
  - 需要时通过平台媒体获取能力下载。
- QQ Official：
  - 同样延迟下载图片。
  - 文件和图片按统一来源描述处理。
- Telegram：
  - 修改当前 `normalizeMessage` 中收到图片后立即 `getFile` 和下载 data URL 的行为。
  - 只在真正需要时取得 file path 并下载。
- 平台发送逻辑继续使用现有 `delivery.Source`。
- 只有 `send_file media` 或 Media Center 管理的输出才新增适配分支。

**Implementation Notes**

实施顺序严格按以下阶段执行：

1. **ElBot 核心与 Media Center**
   - 定义 `media:<sha256>`。
   - 实现 Media Manager、Local Backend、S3/R2 Backend。
   - 复用 `FileDeliveryConfig`。
   - 实现请求级 base64 总大小判断。
   - 实现 LLM resolver。
   - 扩展 LLM segment 和 Session transcript。
   - 实现 Media Storage 与通用引用表。
   - 接入 Agent 真正唤起后的物化时机。
   - 增加媒体清理。
   - `send_file` 增加 `media`，保留原有来源行为。

2. **Hook 协议与 Hook runtime**
   - 复用现有 segment 来源字段承载完整 `media:<sha256>`，不新增独立媒体字段。
   - 增加宿主内 Media API。
   - 增加外部 Hook 的媒体协议。
   - 处理 Hook 输入、输出和引用关系。
   - 保持原有 `url/path/base64` 兼容。

3. **Elnis**
   - 增加 Media Center 与 workspace 的双向按需导入/导出。
   - 保持 Elnis 工作文件独立。
   - 只处理需要跨边界的输入和输出媒体。

4. **Skill**
   - 接入宿主 Media API。
   - 支持 Skill 读取媒体、导出媒体、导入结果文件。
   - 统一处理 Skill 结果的媒体引用。

5. **消息平台适配**
   - 修改 QQ OneBot、QQ Official、Telegram。
   - Chat History 保存原始平台媒体引用。
   - 延迟到真正唤起 ElBot 时才下载和物化。
   - 各平台只实现自己的来源获取和输出适配。

6. **文档和验证**
   - 更新 `docs/*.md` 中的用户可见配置、Hook、Skill、Elnis 和 `send_file` 行为。
   - 更新 `devdocs/architecture.md` 和 `devdocs/code-map.md`。
   - 不修改英文镜像文档。
   - 用户已更新 `CHANGELOG.md`，不用再更。

代码边界要求：

- 不处理旧 Session 的媒体迁移。
- 不批量下载旧 URL。
- 不增加新的媒体配置项。
- 不增加随机媒体 ID。
- 不实现公共 R2 Bucket。
- 不引入自定义媒体代理。
- 不把 Elnis workspace 全部纳入 Media Center。
- 不复制多套 base64、S3/R2、大小限制和媒体读取逻辑。
- 不让平台适配器决定 LLM 使用 data URL 还是 R2 URL。
- 不让 LLM provider 直接依赖平台 URL。
- 不让 Session 持久化 presigned URL。

**Test Plan**

核心 Media Center：

- 相同二进制内容生成同一个 `media:<sha256>`。
- 不同内容生成不同 Media ID。
- MIME、文件名和大小元数据正确保存。
- 本地后端可以导入、读取、导出和删除。
- S3/R2 后端可以上传、生成 presigned GET URL 和删除。
- `backend = "base64"` 在总大小未超限时生成 data URL。
- `backend = "base64"` 超过总大小时返回明确错误。
- `backend = "s3"` 始终走远端后端。
- `backend = "hybrid"` 在总大小阈值两侧整体切换。
- 多张图片总大小判断正确。
- 生成的签名 URL不会写入 Session 或数据库。
- Media Center 重启后可以通过 `media:<sha256>` 找到媒体。

Storage 和引用：

- 媒体元数据 migration 可重复执行。
- 通用引用支持不同 `owner_type`。
- 相同 owner 的重复引用不会重复插入。
- 删除 owner 后引用正确清理。
- 仍有引用的媒体不会被清理。
- 只有临时引用且已过期的媒体可以清理。
- 孤儿媒体可以被清理。
- 引用修复/校准逻辑可以发现不一致。

Agent Core：

- 未唤起的普通平台消息不会下载图片。
- 真正进入命令、Hook continuation 或 LLM 流程时会物化需要使用的媒体。
- 新 Session 消息保存 `media`，而非 QQ 临时 URL。
- 工具结果媒体保存 `media`。
- 视觉模型请求能从 `media` 恢复图片。
- 视觉失败时保留正确的文本投影。
- 媒体不可用时不会伪造视觉结果。
- 多轮 Session 恢复后仍能使用媒体。

Hook：

- Hook 在现有来源字段中填写完整 `media:<sha256>` 修改消息。
- Hook 使用 URL、path、base64 的旧行为仍可用。
- Hook 可以导入媒体。
- Hook 可以读取小媒体。
- Hook 可以把大媒体导出为临时文件。
- Hook 来源字段中的完整 `media:<sha256>` 可以通过平台发送。
- Hook 临时引用和 transcript 引用生命周期正确。

Elnis：

- Elnis 普通工作文件仍只存在 workspace。
- Elnis 可以导出 Media Center 媒体到 workspace。
- Elnis 可以将 workspace 文件导入 Media Center。
- Elnis 只在需要跨边界时创建媒体资源。
- Elnis 原有 report path 行为不被破坏。

Skill：

- Skill 可以通过协议读取媒体元数据。
- Skill 可以读取小媒体。
- Skill 可以导出大媒体。
- Skill 返回的文件可以导入并形成 `media:<sha256>`。
- Skill 结果媒体可以写入 Tool Transcript。

平台：

- QQ OneBot Chat History 保存原始媒体引用。
- QQ Official Chat History 保存原始媒体引用。
- Telegram Chat History 保存原始媒体引用。
- 未唤起时不下载图片。
- 唤起后可以通过各平台来源重新获取图片。
- 平台发送 `media` 不影响现有 `send_file` 来源。
- 现有图片、文件和语音发送测试继续通过。

验证命令：

- 修改 Go 文件后运行 `gofmt`。
- 先运行受影响包测试。
- 核心跨层修改完成后运行完整 `go test ./...`。
- 修改文档后使用 `rg -n` 检查关键配置、API、引用和禁改说明。
- 最终检查 `git diff`，确保没有无关改动。

**Assumptions**

- `file_delivery.backend` 的 `s3` 实现会继续使用现有配置字段，并支持 Cloudflare R2 的 S3 兼容 endpoint。
- S3/R2 的 presigned GET URL 最大不超过 7 天；代码默认使用短期有效期，不新增配置。
- LLM provider 的具体“支持 data URL / 只支持远程 URL”能力，第一版通过现有 OpenAI-compatible 请求模式和 `file_delivery.backend` 决定。
- 平台原始 URL 和 file ID 可能过期，Chat History 只作为原始事实记录，不保证可以永久重新下载。
- 新增通用引用表后，未来可以继续扩展非 Message owner，不需要重新设计引用模型。
- Elnis 的 workspace 和 Media Center 是两个并行资源域，通过 API 交互。
- 旧 Session 不进行迁移；旧 URL 只保留读取兼容。
- `media:<sha256>` 是唯一对外媒体身份，数据库不再保存额外随机 ID 到 hash 的映射。
- `max_direct_base64_bytes` 只作为本次请求的 base64 总大小阈值；单文件最大大小继续遵循现有平台、Hook、Elnis 和其他输入限制。
- 第一阶段先保证核心 API 和本地/R2 访问路径正确，再接入各个平台，避免平台细节反向污染核心设计。

## 计划维护规则（新增，优先适用于后续所有计划操作）

- 本文件是用户维护的独立计划详情；用户已允许直接修正已确认过时或冲突的内容，未获授权的内容仍不得删改。active plan 可以使用 plan_revise 管理，但须先展示完整修订内容并获得确认，不能用工具状态覆盖本文件。
- 任何可能改变、删除、重命名、合并、拆分或重排原有计划内容的操作，必须先明确展示拟议变更，并征得用户同意。
- 未经用户明确同意，不得覆盖、重写、精简、恢复默认版本或删除原有 detail。
- 如果新需求与原计划冲突，先暂停执行并向用户说明冲突；只有用户同意后才能修改冲突部分。
- 如果新需求只是补充且不冲突，应只追加补充内容，不改写原文。
- 勾选已完成项属于执行状态更新，不视为修改任务语义；但不得借此重写其他任务或 detail。

## 本次追加补充：Hook 与 Skill 媒体适配

以下内容是在原 active plan detail 基础上的追加，不替换、不删减原有 Summary、Key Changes、Implementation Notes、Test Plan 或 Assumptions。

### Hook 来源字段复用

Hook 不新增独立的 `media` 字段，也不升级 hook.v2 版本。现有 image/file segment 中原本用于 `url`、`path`、`base64` 或统一 source 的位置，如果其完整值本身是 `media:<sha256>`，则按 Media Center 媒体来源处理。

规则：

- 完整 `media:<sha256>` 可作为现有媒体来源字段的值；
- 与其他来源字段互斥；
- 只识别完整合法的媒体 ID；
- 不扫描普通文本；
- 不自动改写普通 URL、普通路径或包含媒体 ID 的字符串；
- 旧的 URL、path、base64 行为继续兼容；
- 仍不提供 `media.delete`；
- 进程内 Go Hook 继续通过 `Event.Media` 使用宿主 Media API；
- 外部 Hook 继续使用现有 hook.v2 媒体方法。

### Skill 运行形态与媒体边界

Skill 按实际运行方式分为普通 Agent Skill、bash 型 Skill、TOML 配置型工具 Skill 和 Go Skill。稳定媒体身份统一使用完整的 `media:<sha256>`。

普通 Agent Skill 本身不直接访问 Media Center。它可以在 prompt 中指导 Agent 将媒体 ID 传给支持媒体的工具，也可以指导 Agent 显式执行受控媒体导出命令。

通过 bash 执行的 Skill：

- 通过 shell Tool Runtime 的 `media_inputs` 显式声明媒体，每项使用 `media` 字段；
- 必须显式传入完整媒体 ID，并通过 `ELBOT_MEDIA_N` 环境变量使用宿主导出的路径；
- 导出位置由宿主在 sandbox 专用缓存内决定，不提供目标路径或覆盖参数；
- 不暴露 Media Center 根目录、SQLite、S3 凭据或 presigned URL；
- 不提供媒体删除；
- 不自动扫描或改写普通 bash 命令；
- 导出的临时文件由 workspace/sandbox 生命周期清理。

TOML 配置型工具 Skill：

- 可以声明某些参数为媒体参数，例如 `type = "media"`；
- LLM 看到并传入稳定的 `media:<sha256>`；
- Tool Runtime 只处理显式声明为媒体的参数，不扫描所有字符串；
- Tool Runtime 在调用期间校验媒体 ID、建立临时引用、导出到受控临时目录，并将参数转换为工具可读取的临时相对路径；
- 调用结束后清理临时文件并释放调用期引用；
- 工具输出在跨越 Tool Transcript、Session、后续调用或平台输出边界时导入 Media Center。

Go Skill：

- 继续使用现有 `go_skill_run` 的一次性 stdin/stdout 模型；
- 不新增 HTTP 或 RPC 媒体服务；
- payload 可以显式声明媒体 ID；
- `go_skill_run` 负责校验媒体 ID、建立调用期引用、导出到调用专属临时目录、向 Go Skill 提供受控相对路径、解析输出、按需导入结果并清理；
- Go Skill 不直接访问 Media Center 根目录、SQLite、S3 凭据或 presigned URL；
- Go Skill 输出优先使用受控 workspace 相对路径，跨边界时由宿主导入并形成 `media:<sha256>`。

### Skill 接口选择

当前阶段不实现全局媒体 HTTP API、RPC 服务或 Skill 专用双向媒体协议。

- 普通 bash Skill 使用 shell Tool Runtime 的 `media_inputs`；
- TOML 配置型工具由 Tool Runtime 在宿主内完成媒体转换；
- Go Skill 由 `go_skill_run` 完成一次性媒体桥接；
- 只有未来出现长期运行、远程或流式 Skill 的实际需求时，才重新评估 RPC/HTTP。

### 当前执行顺序补充

- Hook 与 Skill 阶段已完成；当前统一媒体字段时同步更新相关 API、测试和文档。
- 当前继续执行 Elnis 阶段及媒体引用生命周期审查。
- Elnis 阶段完成后暂停，不开始平台适配。

### 补充测试标准

- Hook 现有 `url`、`path` 或 `base64` 字段填写完整 `media:<sha256>` 时能被正确识别；
- Hook 媒体 ID 与其他来源混用时拒绝；
- 普通文本中的媒体 ID 不被扫描；
- bash Skill 可将合法媒体导出到受控 workspace；
- bash Skill 无法通过绝对路径、`..` 或 symlink/junction 逃逸；
- TOML 工具只转换显式声明为媒体的参数；
- Go Skill 可通过 `go_skill_run` 获得调用期临时文件；
- Go Skill 在成功、失败、超时后都释放临时引用并清理临时文件；
- Skill 结果文件可以在跨边界时导入并形成稳定 `media:<sha256>`；
- 非媒体 Hook、Tool 和 Skill 行为保持不变；
- 不需要 HTTP/RPC 服务即可完成上述本地 Skill 媒体流程。

## 原计划内容保留声明

## 本轮追加补充：bash Skill 通过 shell Tool Runtime 使用媒体

本节是对前述 Skill 约定的补充；如前文 bash Skill 的示例与本节冲突，以本节为准，原文其余内容保留。

所有 bash 型 Skill 的 shell 调用仍然经过 Tool Runtime，因此不需要独立 `elbot-media` CLI、HTTP 或 RPC。shell 工具增加可选的 `media_inputs` 参数，仅用于显式声明本次命令需要的媒体；不扫描或改写 `cmd`，不影响 LLM 看到的原始工具调用和 Tool Transcript。

示例：

```json
{
  "cmd": "python generate.py --input $ELBOT_MEDIA_1 --output result.png",
  "media_inputs": [
    {"media": "media:<完整SHA-256>"}
  ]
}
```

Tool Runtime 在执行副本中：

- 校验每个完整 `media:<sha256>`；
- 在 sandbox 的专用媒体输入目录中按 SHA-256 文件名和媒体元数据后缀导出或复用文件；
- 向本次 shell 进程注入 `ELBOT_MEDIA_1`、`ELBOT_MEDIA_2` 等路径变量；
- 原样执行 `cmd`，由 Skill 显式使用这些变量；
- 不把真实路径写回 LLM 调用参数、Session 或 Tool Transcript；
- 媒体准备失败时不执行命令；
- 成功、失败、取消或超时后释放调用期引用。

`media_inputs` 只有 `media` 字段，不提供目标 `path` 或 `overwrite`。导出文件名由宿主根据媒体 ID 和元数据决定，避免文件覆盖参数及路径逃逸问题。媒体导出目录不使用 Agent 或 Skill 的工作目录作为目标输入，而位于 `data/sandbox` 下的专用目录；脚本通过环境变量取得路径。

导出缓存复用 sandbox 按文件 ModTime 清理的机制，每次使用（包括命中缓存）刷新修改时间，不新增缓存活动计数、使用中锁或专用清理。删除导出副本不删除 Media Center 媒体本体；Media Center 本体仍只由通用媒体引用和孤儿清理规则管理。普通 shell 调用不传 `media_inputs` 时行为完全不变。

Agent Skill Creator 的说明只在生成需要媒体的 Skill 文档中介绍 `media_inputs` 和 `ELBOT_MEDIA_N` 的用法，不修改普通 shell 工具的通用 schema 描述之外的其他工具 schema。Go Skill 和 TOML 配置型 Skill 继续沿用前述既定方案。

shell Tool Runtime 的 `media_inputs`、sandbox 导出缓存、Go Skill 和 TOML 配置型 Skill 已完成；当前同步统一媒体字段，并继续 Elnis 阶段。

不能擅自修改计划。用户有新需求或者需求和之前plan有冲突的，和用户讨论完毕后，征得用户明确同意后可修改（如删除冲突、更新过时内容）。

## 本轮确认补充：Skill 导出缓存与文档范围

本节优先于前文缓存保护与清理约定。shell 媒体导出缓存继续使用既有 sandbox 按文件 ModTime 清理的机制；每次显式使用（包括缓存命中和只读使用）刷新导出副本的修改时间。不新增使用中锁、活动计数或专用清理。Media Center 调用期引用仍在调用结束、失败、取消或超时后释放。

实施顺序保持 shell 显式 media_inputs、sandbox 缓存、Go Skill/TOML 媒体输入输出、说明与验证。收窄现有 ToolRun 任意参数完整媒体 ID 自动转换为显式声明处理，风险评估不导出媒体，原始调用记录不改写。验证无效输入、缓存复用与时间刷新、过期清理、生命周期清理、受控输出路径和媒体 transcript。

Skill 说明更新 internal/config/assets.go 及必要中文文档、开发文档；CHANGELOG.md 由用户维护，本轮不修改。第 6 项 Skill 已完成；用户已确认继续 Elnis，完成后暂停。

## 本轮确认补充：执行第 5 项 Elnis

Summary：用户确认继续第 5 项，采用讨论稿的 direct 输出进入 Media Center、Elnis workspace 保持独立方案。引用记录为事实来源，不维护整数计数。第 6 项 Skill 已完成，保留勾选；第 5 项完成后暂停，第 7 项平台引用回复消费另做。

Key Changes：Elvena v3 image/file 复用 url 承载 HTTP(S)、data URI 或完整 media:<sha256>，不新增独立媒体字段；不开放媒体 RPC，不向 Elwisp 远端工具导出宿主媒体。Elnis direct 实际有目标才导入/复用媒体，不在 sandbox 保留下载副本。LLM 输入在实际执行时物化；record/拒绝/重复/仅排队的 URL 不下载。报告附件保留相对 workspace 路径语义，持久化发送前安全导入；outbox 保存稳定来源。修复 data URI 文件名重复扩展名。新增平台+会话范围+消息 ID 的有限期输出关联、引用与媒体清理，修改必要的 storage/sqlite、media、elnis、app/maintenance 和文档。

Implementation Notes：发送期间保护、待重试保留引用、成功后按精确回执建立缓存；缓存期限复用 sandbox retention_days，非正值不保留已发送缓存。Session/Transcript 独立持有引用。持久化 owner 与引用同事务，交接先建新引用后释放旧引用，引用添加与孤儿清理认领互斥，后端删除失败保持记录可重试；孤儿宽限期固定 1 小时，从失去最后引用起算并持久化 orphaned_at；维护任务按时间戳判断，关机时间计入宽限期，重启不重置计时。检查多 Session、fork、owner 删除及临时引用崩溃恢复，并提供只读一致性检查。保留已发布 HTTP(S)、data URI 等来源行为，不为尚未发布的旧媒体 ID JSON 字段保留兼容分支；不新增配置，不迁移旧 Session。CHANGELOG.md 仍由用户维护。

Test Plan：先复现双后缀；覆盖 PNG 大小写/无后缀/无名称、多 Session 和 fork 共享媒体、direct 无目标不下载及无 sandbox 副本、非法或混合来源、workspace 路径与链接逃逸、报告文件删除后 outbox 重试、重复回执及范围隔离、部分发送成功、缓存过期、非正期限、引用与清理并发、后端删除失败重试和一致性检查。gofmt、相关包测试、go test ./...、文档 locator/link 检查与 git diff --check。

Assumptions：Chat History 的原始 URL/file ID 不构成中心引用；普通工作文件不入库。第 5 项提供关联写入和查询能力，不宣称已打通平台引用回复。用户已确认本轮范围及引用模型并授权开始。
