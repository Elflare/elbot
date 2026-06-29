package elyph

const (
	// SkillFileName 是 ElBot 原生 skill 的强结构说明文件名。
	SkillFileName = "SKILL.elyph"
	Format        = "elyph"
)

// RuleCard 返回 discover ELyph skill detail 时按需注入给 LLM 的极短规则卡。
func RuleCard() string {
	return `ELyph v0.3规则：非空行首必须是#,//,<-,->,$,=>,**,~,?if,?else,each,step,>,@tool,@skill,}；符号：#头；//整行注释；<-输入；->输出；$直接赋值；=>推导；**约束；~禁止（内容不再用否定形式）；?if/?else分支；each限次循环；step命名阶段（名首字符小写字母或数字，禁嵌套/空/重名；单句用 step name: 语句 单行形式，块内至少2句）；>输出文本；@tool工具；@skill技能；}闭块。#skill/#task 名称 - 描述；IO：<- $x:type!、-> $y:type；+按左侧type，int/num相加，str拼接；推导三元：=> $x = 条件 ? 真 : 假；块用{ }且}独行。
** 注：ELyph内容应精简、保持少歧义。
例：
// 注释只能整行
#skill weather - 查天气
<- $city:str!
<- $days:list?
-> $remind:str
** 根据查询结果判断
~ 编造天气
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
each($day in $days, limit=3) {
  @skill weather_day(city=$city, day=$day)
}`
}
