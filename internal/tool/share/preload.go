// 定义工具、Skill 和后台预加载的请求与结果。
package share

type ToolPreload struct {
	// NeedsElyphRules is merged with other material before preparing the input.
	NeedsElyphRules bool
	Update          StateUpdate
	Accepted        []bool
	Invalid         []string
	Content         string
}

type SkillPreload struct {
	// NeedsElyphRules reflects the actual formats of loaded skills.
	NeedsElyphRules bool
	Update          StateUpdate
	Accepted        []bool
	Names           []string
	Invalid         []string
	Content         string
}

// BackgroundPreloadRequest 只提供工具材料，不携带任务来源业务对象。
type BackgroundPreloadRequest struct {
	SessionID        string                  // 预载审计关联的会话。
	Names            []string                // 本地工具、Skill 或标签名称。
	AllowedToolNames []string                // 上游给定的本地根工具范围；nil 不额外限制，空切片不允许。
	ELwispTools      []ELwispToolDeclaration // 已由 Elnis 适配的外部工具声明。
}

type BackgroundPreload struct {
	// NeedsElyphRules covers both preloaded tools and skills.
	NeedsElyphRules bool
	Update          StateUpdate
	Skills          []string
	// SkillPrompt carries tool usage instructions and Skill documentation for the task input.
	SkillPrompt string
}

// BackgroundSelection resolves task selectors before dependency expansion. The
// same roots are used by Elnis authorization and by the actual task preload.
type BackgroundSelection struct {
	Names []string
	Tag   string
}

// TagPrompt is display material; the caller owns placement in its prompt.
type TagPrompt struct {
	Tag    string
	Prompt string
}
