// 定义供调用方组织提示词的工具与 Skill 名称数据。
package share

type PromptNames struct {
	Tools  []string // 已通过查询筛选的普通工具名称。
	Skills []string // 已通过查询筛选的 Skill 名称。
}
