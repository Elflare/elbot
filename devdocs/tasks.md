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
    - [x] agent 根部（不含子包）
    - [x] dialogue包
    - [x] 第 1 批：hooks、output（与第 2 批合看）
    - [x] 第 2 批：tools、confirmation
    - [x] 第 3 批：input
    - [x] 第 4 批：execution
    - [x] 第 5 批：routes、chat
    - [x] 第 6 批：responses
    - [x] 第 7 批：share（最后按实际消费者核对共享契约）
- [ ] platform包
- [ ] tool包
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
