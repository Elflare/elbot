// 定义工具、Skill 和后台预加载的结果。
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
