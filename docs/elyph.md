# ELyph 任务表示法

ELyph（ELYph Task Notation）是 ElBot 的轻量任务表示法。它的目标是用简短、稳定的结构化语法描述任务的输入、输出、步骤、条件和约束，减少自然语言任务描述的歧义，同时人类易读。

当前版本：**v0.3**。

## 为什么需要 ELyph

自然语言描述任务虽然灵活，但在以下场景存在明显弱点：

- **复用性差**：每次都要重新描述相似流程，写法可能不一致。
- **歧义多**：同一句话可能被理解成不同步骤。
- **上下文长**：用自然语言把步骤、分支、约束写清楚需要大量文字。

ELyph 用固定的语法替代冗长的自然语言说明，让任务描述更短、更稳定、更可预测。

## ELyph 用在哪

| 场景 | 文件 | 说明 |
| --- | --- | --- |
| AgentSkill | `skills/agent/<skill>/SKILL.elyph` 或 `SKILL.md` | 外置文档型 Skill；`SKILL.elyph` 会覆写 Agent 可读说明，`SKILL.md` 兼容 agentskills.io 风格说明。 |
| Go Skill | `skills/go/<skill>/SKILL.elyph` | ElBot 原生 Skill；可选 Go 二进制用于执行。 |

| LLM Cron 任务 | 任务条 | 在 `/cron write` 中用 `#task` 头描述后台任务。 |

## 语法速查

### 基本规则

- 非空行必须以合法的 ELyph token 开头。
- 空行和 `//` 开头的注释行会被忽略。
- 代码块用 `{` 开始，`}` 独占一行结束。
- 变量以 `$` 开头，后跟字母、数字或下划线。
- 类型标注用 `:type`，类型名同变量命名规则。

**保留变量**：`$user` 和 `$assistant` 不能被重新赋值。

---

### Header（文档头）

文档的第一条有效语句必须是 header，且只能出现一次。

```
#skill <name> - <description>
#task <name> - <description>
```

- `<name>`：小写字母开头，只能包含小写字母、数字、`_` 和 `-`，最长 64 字符。
- `<description>`：简短的自然语言描述，可选。

---

### `<-` 输入

声明任务需要接收的输入。

```
<- $name:type
<- $name:type!     必填输入
<- $name:type?     可选输入
```

- `!` 表示该输入必填。
- `?` 表示该输入可选。
- 无标记表示普通输入。

---

### `->` 输出

声明任务产生的输出。

```
-> $name:type
```

---

### `$` 直接赋值

给变量赋值。

```
$name = value
$name:type = value
```

`+` 运算符按左侧类型决定行为：`int`/`num` 类型做数值相加，`str` 类型做字符串拼接。

```
$q:str = $city + 天气
```

---

### `=>` 推导赋值

用表达式推导变量值，支持三元条件。

```
=> $name = expression
=> $name = 条件 ? 真值 : 假值
```

三元表达式的三个部分都不能为空，条件部分通常引用上一步工具调用的结果。

---

### `**` 约束

写出任务执行时需要遵守的约束条件。内容使用肯定形式。

```
** 根据查询结果判断是否下雨
** 只搜索最近 3 天的数据
```

---

### `~` 禁止

写出任务执行时不能做的事。内容不使用否定形式，直接描述禁止的行为本身。

```
~ 编造天气数据
~ 使用未经验证的来源
```

---

### `?if` / `?else` 条件分支

```
?if(condition) {
  ...statements...
}
?else {
  ...statements...
}
```

- `?else` 必须紧跟 `?if` 块的 `}` 之后。
- 条件用自然语言或引用变量，由 LLM 在执行时理解。

---

### `each` 限次循环

```
each($item in $items, limit=N) {
  ...statements...
}
```

- `limit` 必须是正整数。
- 循环内可以调用 `@skill` 或 `@tool`。

---

### `step` 命名阶段

把流程拆成命名阶段，可选使用，不强制全包；顶层允许裸语句与 `step` 混用。

**块形式**（至少 2 句）：

```
step <name> {
  ...statements...
}
```

**单行形式**（仅 1 句）：

```
step <name>: <statement>
```

- `<name>`：首字符是小写字母或数字，其余可含小写字母、数字、`_`、`-`，最长 64。可为纯数字（如 `step 1`）。
- `step` 只能出现在顶层，**不能嵌套** `step`。
- `step` **不能为空**。
- **块内至少 2 句**；只有 1 句时必须用单行形式 `step name: 语句`。
- 单行形式的语句必须是简单单行语句（`$`/`=>`/`>`/`@tool`/`@skill`/`**`/`~`/`<-`/`->`），不能是 `?if`/`?else`/`each`/`step`/`}`。
- 同一文档内 `step` 名**不能重复**。
- 块内可使用 `?if`/`?else`/`each`/`$`/`=>`/`@tool`/`@skill`/`>`/`**`/`~` 等任意语句。

---

### `>` 输出文本

输出一段文本。

```
> 今天不用带伞
> 查询完成，结果如下：
```

---

### `@tool` / `@skill` 工具调用

调用 ElBot 工具或子 skill。

```
@tool tool_name(key=value, key2=value2)
@skill skill_name(key=value)
```

参数使用 `key=value` 格式，多个参数用 `,` 分隔。

---

### `}` 闭块

结束一个代码块，独占一行。

```
}
```

---

### `//` 注释

只能整行注释。

```
// 这是注释
```

---

## 完整示例

### 示例一：天气查询 skill

```
#skill weather - 查询城市天气并提醒是否带伞
<- $city:str!
-> $remind:str

** 根据查询结果判断
~ 编造天气数据

step fetch {
  $q:str = $city + 天气
  $w = @tool web_search(query=$q)
}
step decide {
  => $rain = $w 是否下雨 ? 是 : 否
  ?if($rain) {
    > 记得带伞
  }
  ?else {
    > 今天不用带伞
  }
}
step notify: > 已通知 $city
```

### 示例二：多城市天气查询

```
#skill weather_multi - 一次查询多个城市天气
<- $cities:list!
-> $report:str

** 每个城市独立查询
~ 混用不同数据源

each($city in $cities, limit=5) {
  @skill weather(city=$city)
}
> 以上为所有查询结果
```

---

## 与 Skill 的关系

- Skill 的 `SKILL.elyph` 文件使用 `#skill` header。
- AgentSkill 放在 `skills/agent/<skill>/`，可用 `SKILL.elyph` 覆写 `SKILL.md` 的 Agent 可读说明。
- Go Skill 放在 `skills/go/<skill>/`，必须使用 `SKILL.elyph`，可选 Go 二进制用于执行。
- 如果同一 AgentSkill 同时存在 `SKILL.md` 和 `SKILL.elyph`，ELyph 文件的描述会优先被 Agent 使用。


更多 skill 管理操作见 [命令速查](commands.md) 的 `/tools` 部分。

## 与 Cron 的关系

LLM Cron 任务可以用 `#task` header 写任务条，语法与 `#skill` 完全相同。Cron 按计划驱动 LLM 执行任务条中的步骤，并使用指定的工具。

---

## 语法演进

ELyph 当前是 v0.3，语法可能在未来版本中扩展。写 `SKILL.elyph` 或任务条时，建议保持结构简洁。
