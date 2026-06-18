<!-- This file is auto-translated from docs/elyph.md. Do not edit manually. -->

# ELyph Task Notation

ELyph (ELyph Task Notation) is ElBot's lightweight task notation. Its goal is to describe the input, output, steps, conditions, and constraints of a task using a short, stable, and structured syntax, reducing the ambiguity of natural language task descriptions while remaining human-readable.

Current version: **v0.2**.

## Why ELyph is Needed

While describing tasks in natural language is flexible, it has obvious weaknesses in the following scenarios:

- **Poor reusability**: Similar processes must be described every time, and the wording may be inconsistent.
- **High ambiguity**: The same sentence may be interpreted as different steps.
- **Long context**: Using natural language to clearly describe steps, branches, and constraints requires a large amount of text.

ELyph replaces verbose natural language descriptions with a fixed syntax, making task descriptions shorter, more stable, and more predictable.

## Where to use ELyph

| Scenario | File | Description |
| --- | --- | --- |
| AgentSkill | `skills/agent/<skill>/SKILL.elyph` or `SKILL.md` | External documentation-based Skill; `SKILL.elyph` overwrites the Agent-readable description, and `SKILL.md` is compatible with agentskills.io style descriptions. |
| Go Skill | `skills/go/<skill>/SKILL.elyph` | ElBot native skill; optional Go binary for execution. |

| LLM Cron task | Task entry | Describe background tasks in `/cron write` using the `#task` header. |

## Syntax Quick Reference

### Basic Rules

- Non-empty lines must start with a valid ELyph token.
- Empty lines and comment lines starting with `//` will be ignored.
- Code blocks start with `{` and end with `}` on a line by itself.
- Variables start with `$`, followed by letters, numbers, or underscores.
- Type annotations use `:type`, and type names follow the same naming rules as variables.

**Reserved Variables**: `$user` and `$assistant` cannot be reassigned.

---

### Header

The first valid statement of the document must be a header, and it can only appear once.

```
#skill <name> - <description>
#task <name> - <description>
```

- `<name>`: Starts with a lowercase letter and can only contain lowercase letters, numbers, `_`, and `-`, with a maximum length of 64 characters.
- `<description>`: A short natural language description, optional.

---

### `<-` Input

Declare the input that the task needs to receive.

```
<- $name:type
<- $name:type!     必填输入
<- $name:type?     可选输入
```

- `!` indicates that this input is required.
- `?` indicates that this input is optional.
- No marker indicates a regular input.

---

### `->` Output

Declare the output produced by the task.

```
-> $name:type
```

---

### `$` Direct Assignment

Assign a value to a variable.

```
$name = value
$name:type = value
```

The `+` operator determines its behavior based on the type of the left operand: `int`/`num` types perform numeric addition, while `str` types perform string concatenation.

```
$q:str = $city + 天气
```

---

### `=>` Derived Assignment

Derive variable values using expressions, with support for ternary conditions.

```
=> $name = expression
=> $name = 条件 ? 真值 : 假值
```

All three parts of a ternary expression must be non-empty; the condition part typically references the result of the previous tool call.

---

### `**` Constraint

Specify the constraints that must be followed during task execution. Content should be written in affirmative form.

```
** 根据查询结果判断是否下雨
** 只搜索最近 3 天的数据
```

---

### `~` Prohibition

Specify things that must not be done during task execution. Do not use negative forms; describe the forbidden behavior directly.

```
~ 编造天气数据
~ 使用未经验证的来源
```

---

### `?if` / `?else` Conditional Branching

```
?if(condition) {
  ...statements...
}
?else {
  ...statements...
}
```

- `?else` must immediately follow the `}` of the `?if` block.
- Conditions are written in natural language or by referencing variables, which are understood by the LLM during execution.

---

### `each` Limited Loop

```
each($item in $items, limit=N) {
  ...statements...
}
```

- `limit` must be a positive integer.
- `@skill` or `@tool` can be called within the loop.

---

### `>` Output Text

Output a piece of text.

```
> 今天不用带伞
> 查询完成，结果如下：
```

---

### `@tool` / `@skill` tool call

Call an ElBot tool or sub-skill.

```
@tool tool_name(key=value, key2=value2)
@skill skill_name(key=value)
```

Parameters use the `key=value` format, and multiple parameters are separated by `,`.

---

### `}` closing block

Ends a code block, occupying its own line.

```
}
```

---

### `//` comment

Full-line comment, which will be ignored.

```
// 这是注释
```

---

## Complete Examples

### Example 1: Weather Query Skill

```
#skill weather - 查询城市天气并提醒是否带伞
<- $city:str!
-> $remind:str

** 根据查询结果判断
~ 编造天气数据

$q:str = $city + 天气
$w = @tool web_search(query=$q)
=> $rain = $w 是否下雨 ? 是 : 否
?if($rain) {
  > 记得带伞
}
?else {
  > 今天不用带伞
}
```

### Example 2: Multi-city Weather Query

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

## Relationship with Skill

- The `SKILL.elyph` file of a Skill uses the `#skill` header.
- AgentSkill is placed in `skills/agent/<skill>/`, and `SKILL.elyph` can be used to override the Agent-readable description of `SKILL.md`.
- Go Skill is placed in `skills/go/<skill>/`, must use `SKILL.elyph`, and an optional Go binary can be used for execution.
- If the same AgentSkill has both `SKILL.md` and `SKILL.elyph`, the description in the ELyph file will be prioritized for use by the Agent.


For more Skill management operations, see the `/tools` section of [Command Quick Reference](commands.md).

## Relationship with Cron

LLM Cron tasks can use the `#task` header to write task entries, and the syntax is identical to `#skill`. Cron drives the LLM to execute the steps in the task entry according to a schedule, using the specified tools.

---

## Syntax Evolution

ELyph is currently v0.2, and the syntax may be expanded in future versions. When writing `SKILL.elyph` or task entries, it is recommended to keep the structure concise and avoid relying on experimental notations not listed in this document.
