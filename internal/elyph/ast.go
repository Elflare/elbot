package elyph

// Document 是 ELyph 文档的轻量解析结果；当前只承载 skill 创建和扫描所需信息。
type Document struct {
	Kind  string
	Name  string
	Steps []Step
}

// Header 是 ELyph 文档首行的轻量解析结果，仅供启动扫描快速登记技能使用，不做全文校验。
type Header struct {
	Kind        string
	Name        string
	Description string
}

// Step 记录一个 step 命名阶段块的元信息，供 scanner/descriptor 列出流程阶段。
type Step struct {
	Name string
	Line int
}

type DiagnosticSeverity string

const (
	DiagnosticError   DiagnosticSeverity = "error"
	DiagnosticWarning DiagnosticSeverity = "warning"
)

// Diagnostic 描述一条可反馈给 LLM 重写 ELyph 的格式问题。
type Diagnostic struct {
	Line     int
	Message  string
	Severity DiagnosticSeverity
}
