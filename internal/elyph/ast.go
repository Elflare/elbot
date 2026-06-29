package elyph

// Document 是 ELyph 文档的轻量解析结果；当前只承载 skill 创建和扫描所需信息。
type Document struct {
	Kind  string
	Name  string
	Steps []Step
}

// Step 记录一个 step 命名阶段块的元信息，供 scanner/descriptor 列出流程阶段。
type Step struct {
	Name string
	Line int
}

// Diagnostic 描述一条可反馈给 LLM 重写 ELyph 的格式问题。
type Diagnostic struct {
	Line    int
	Message string
}
