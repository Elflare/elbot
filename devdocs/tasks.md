
- [ ] 前端显示正在调用工具名字和参数，结束后直接原地改写结果，保留名字和参数，内容和之前一样。需要同步改后端api，平台消息如[tool]xxx统一成control操作，raw提供原始内容，消息平台裁剪为文本，参考command_result_dispatch.go逻辑，给一个共享函数裁剪，也可直接拿raw blocks
- [ ] 加入phase压缩中
- [ ] context_compact.summary 和 user_inputs 则作为再次压缩的结构化记录。下次压缩时，历史消息用于构造上下文，user_inputs 用于保留原话；不要再从整段历史里把用户原话全量重捞，也不要把 summary 再额外注入一次，否则摘要会在压缩材料里重复。

## 不一定实现

- [ ] CLI TUI 支持 Markdown 渲染。
- [ ] CLI 用户名和助手显示名可配置。
- [ ] CLI 支持本地图片输入。
- [ ] 周期 Cron 支持过期时间。


## 暂缓

- MCP 接入：暂不考虑，使用 Skill。
- 子 Agent：暂不考虑，信息交流会带来噪声或损失。
