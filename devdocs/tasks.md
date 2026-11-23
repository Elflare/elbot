## Co-agent 协作

[需求、权限和验收场景](co-agent.md)

- [x] SQLite 原子创建成员、保存置顶和留言、接续压缩后的成员 ID。已验证清空正文、并发编号和事务回滚。
- [x] 接入 `/co` 命令和按角色激活操作的 `coagent` 发现入口。主和成员分别执行，按 Session ID 投递广播或定向留言。
- [x] 用两名成员验证署名回复、主对话隔离、定向收件及广播再次唤醒。
- [x] 限制每组最多 8 个 co-agent，主会话另计。在同一事务中校验现有名额与整批新增数。
- [x] 验证忙时 pending、压缩期间投递、远端 CLI 重连主窗口、并发停止和停止后再次唤醒。
- [x] 验证成员的工具发现、缓存及直接模型调用的资格，检查工作区与操作者权限。
- [x] Co-agent 对所有有权使用的工具免逐项确认，包括 `high`、`critical`。主会话按原有策略确认。

## 业务线

把以下包和5条业务线一样并列
- [ ] control
- [ ] storage
- [ ] logging
- [ ] session 

## 代码精简

目标：精简所有过度防御、无用（或者多余）变量、重复逻辑、过度抽象
构造的无意义、强行找茬测试均删
防御代码：只对竞态、io、网络等不可控地方进行防御，内部代码逻辑都假定不可能出错，真出错就交给报错方便排查
每次看一个包时，列举所有if err之类的判断
生命周期：启动失败直接报错退出；全部启动成功后才负责关闭
每个包分两步，第一步不改变逻辑的精简，第二步可以改变逻辑，只不改变功能，从代码逻辑、架构方面还能怎么精简，主要是去重复逻辑、减少猜测、兜底之类的
- [x] app包
- [x] agent包
- [x] platform包
- [ ] tool包
    - [x] tool 根部（不含子包）
    - [x] builtin
        - [x] builtin 根部（不含子包）
        - [x] builtin/chat_history
        - [x] builtin/long_memory
        - [x] builtin/weave
        - [x] builtin/web
    - [x] discovery
    - [x] execution
    - [ ] media
    - [ ] preload
    - [ ] registry
    - [ ] skill
    - [ ] state
    - [ ] share（最后按实际消费者核对共享契约）
- [ ] hook 包
- [ ] background包
- [x] completion包
- [ ] fileops包
- [ ] workspace包
- [x] llm包
- [ ] media包
- [ ] storage包
- [ ] session包
- [x] command包
- [ ] control包
- [ ] delivery包

## 不一定实现

- [ ] CLI TUI 支持 Markdown 渲染。
- [ ] CLI 用户名和助手显示名可配置。
- [ ] CLI 支持本地图片输入。
- [ ] 周期 Cron 支持过期时间。


## 暂缓

- MCP 接入：暂不考虑，使用 Skill。
- 子 Agent：暂不考虑，信息交流会带来噪声或损失。
