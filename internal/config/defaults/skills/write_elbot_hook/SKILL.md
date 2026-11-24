---
name: write_elbot_hook
description: 编写或修改 ElBot 规则 Hook 配置。
---

hook路径：
windows：%AppData%/ElBot/plugins/hooks.toml
Linux：= $XDG_CONFIG_HOME/elbot/plugins/hooks.toml
若 XDG_CONFIG_HOME 未设置，按 XDG 规范使用 $HOME/.config

简单hook直接参考hooks.toml中的注释写，复杂hook看https://raw.githubusercontent.com/Elflare/elbot/main/docs/hooks.md
修改完hooks.md后提醒用户使用 /hooks reload 重新加载
