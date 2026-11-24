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
- [x] tool包
    - [x] tool 根部（不含子包）
    - [x] builtin
        - [x] builtin 根部（不含子包）
        - [x] builtin/chat_history
        - [x] builtin/long_memory
        - [x] builtin/weave
        - [x] builtin/web
    - [x] discovery
    - [x] execution
    - [x] media
    - [x] preload
    - [x] registry
    - [x] skill
    - [x] state
    - [x] share（最后按实际消费者核对共享契约）
- [ ] hook 包（总管启动失败直接报错退出；单个插件失败只报该插件的错误，不阻断模块启动，修正后可热重载）
    - [x] hook 根部（不含子包）
    - [x] control、pipeline
    - [x] rules
    - [ ] runtime
    - [ ] share/output
    - [ ] share/protocol
    - [ ] share 根部（最后按实际消费者核对共享契约）
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

- [ ] 周期 Cron 支持过期时间。

## 暂缓

- MCP 接入：暂不考虑，使用 Skill。
- 子 Agent：暂不考虑，信息交流会带来噪声或损失。
- TUI 支持 Markdown 渲染、本地图片、配置名字等：暂不考虑，请用web端
