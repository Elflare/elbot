package elyph

const (
	// SkillFileName 是 ElBot 原生 skill 的强结构说明文件名。
	SkillFileName = "SKILL.elyph"
	Format        = "elyph"
)

// RuleCardIfNeeded decides whether this session needs the shared language rules.
func RuleCardIfNeeded(needsElyphRules, elyphRulesShown bool) string {
	if !needsElyphRules || elyphRulesShown {
		return ""
	}
	return "ELyph 规则：\n" + RuleCard()
}

// RuleCard returns the common ELyph rules for skills, tasks and tools.
func RuleCard() string {
	return `ELyph v0.3：供LLM理解的任务表示法，非可执行代码。
写作：
** 不限中英或混用；以最精确、最简短为准，不因缩短引入歧义。
** 逻辑优先用符号、表达式、伪代码；仍须遵守ELyph行首语法。
** 分支用?if/?else，循环用each，调用用@tool/@skill，推导用=>。
** 每行**只表达一个约束；~直接写禁止的行为，不再加否定词。
** 自然语言用于短语义判断、必要说明及面向用户的输出。
~ 用**、//、>包装长段执行流程
~ 重复已声明的IO、步骤、约束
~ 未定义的缩写、函数或工具结果字段
~ 为套格式添加无用变量、step或注释
语法：非空行首为#,//,<-,->,$,=>,**,~,?if,?else,each,step,>,@tool,@skill,}。
#skill/#task name - 简述：首条有效语句，全文仅一个header。
<- $x:type!：必填输入；?：可选输入；无标记：普通输入。
-> $y:type：输出。
$x[:type] = value：直接赋值；=> $x = expr：推导，三元为condition ? value1 : value2。
+按左侧type：int/num→相加，str→拼接；不重赋值$user/$assistant。
** rule：原子约束，非Markdown加粗；~ action：禁止的行为。
?if(condition) { ... }；?else紧跟对应闭块。
each($x in $xs, limit=N) { ... }：N为正整数。
step name: stmt：单句简单语句；step name { ... }：至少2句。
step仅顶层，不嵌套/空/重名；名首字符为小写字母或数字。
@tool name(k=v) / @skill name(k=v)：调用。
> text：输出内容，不用于代写执行指令；// text：整行注释。
块以{开始，}独占一行。
例：
#task weather_batch - 批量天气提醒
<- $cities:list!
<- $prefix:str?
<- $unit:str
-> $summary:str
// weather_advice假设为已发现的雨天出行建议Skill
** 各城市独立查询、判断
~ 编造天气
step init {
  => $label:str = $prefix已提供 ? $prefix : "天气提醒"
  $count:int = 0
}
each($city in $cities, limit=3) {
  $q:str = $city + " 天气 " + $unit
  @tool web_search(query=$q, max_results=3)
  => $rain:bool = 本次结果明确预报降雨
  ?if($rain) {
    @skill weather_advice(city=$city)
    > $city：记得带伞
  }
  ?else {
    > $city：未确认降雨，出发前复查
  }
  $count = $count + 1
}
$summary:str = $label + "完成"
step finish: > $summary；已处理$count个城市`
}
