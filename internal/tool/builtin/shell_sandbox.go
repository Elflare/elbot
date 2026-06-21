package builtin

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"elbot/internal/security"
	"elbot/internal/tool"
	"mvdan.cc/sh/v3/syntax"
)

var windowsAbsPathPattern = regexp.MustCompile(`^[A-Za-z]:/`)

// background sandbox 是轻量路径约束，不是完整 OS 级沙箱。
// 后台任务无人值守，因此禁止绝对路径、.. 逃逸、动态路径和 cd，
// 尽量把 shell 影响限制在 sandbox cwd 内。
func applyShellSandboxRisk(cmdText string, assessment tool.RiskAssessment) tool.RiskAssessment {
	violations := validateShellSandboxCommand(cmdText)
	if len(violations) == 0 {
		return assessment
	}
	if security.CompareRisk(tool.RiskCritical, assessment.Level) > 0 {
		assessment.Level = tool.RiskCritical
	}
	assessment.Reasons = appendUniqueReasons(assessment.Reasons, violations)
	return assessment
}

func validateShellSandboxCommand(cmdText string) []string {
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(cmdText), "")
	if err != nil {
		return []string{"background sandbox 无法解析 shell AST，拒绝后台自动执行: " + err.Error()}
	}
	checker := &shellSandboxChecker{}
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case nil:
			return true
		case *syntax.Redirect:
			checker.checkRedirect(n)
		case *syntax.CallExpr:
			checker.checkCall(n)
		}
		return true
	})
	return checker.violations
}

type shellSandboxChecker struct {
	violations []string
}

func (c *shellSandboxChecker) checkRedirect(redir *syntax.Redirect) {
	if redir == nil || redir.Word == nil {
		return
	}
	if !strings.Contains(redir.Op.String(), ">") && !strings.Contains(redir.Op.String(), "<>") {
		return
	}
	c.checkPathWord(redir.Word, "重定向目标")
}

func (c *shellSandboxChecker) checkCall(call *syntax.CallExpr) {
	if call == nil || len(call.Args) == 0 {
		return
	}
	name, ok := literalWord(call.Args[0])
	if !ok || name == "" {
		c.add("命令名包含动态结构，background sandbox 无法静态确认")
		return
	}
	name = commandBase(name)
	if name == "cd" {
		c.add("后台 shell 不允许 cd")
		return
	}
	if !sandboxPathCommand(name) {
		return
	}
	for _, word := range call.Args[1:] {
		arg, ok := literalWord(word)
		if !ok {
			c.add(fmt.Sprintf("%s 的路径参数包含变量、命令替换或其他动态结构", name))
			continue
		}
		if arg == "" || strings.HasPrefix(arg, "-") {
			continue
		}
		c.checkPath(arg, name+" 参数")
	}
}

func (c *shellSandboxChecker) checkPathWord(word *syntax.Word, label string) {
	value, ok := literalWord(word)
	if !ok {
		c.add(label + "包含变量、命令替换或其他动态结构")
		return
	}
	c.checkPath(value, label)
}

func (c *shellSandboxChecker) checkPath(value, label string) {
	if reason := sandboxPathViolation(value); reason != "" {
		c.add(label + reason)
	}
}

func (c *shellSandboxChecker) add(reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return
	}
	for _, existing := range c.violations {
		if existing == reason {
			return
		}
	}
	c.violations = append(c.violations, reason)
}

func sandboxPathCommand(name string) bool {
	switch name {
	case "cat", "head", "tail", "less", "more", "ls", "touch", "mkdir", "rm", "rmdir", "cp", "mv", "tee", "find", "grep", "sed":
		return true
	default:
		return false
	}
}

func sandboxPathViolation(value string) string {
	p := strings.TrimSpace(value)
	p = strings.Trim(p, "'\"")
	p = strings.ReplaceAll(p, "\\", "/")
	if p == "" || strings.HasPrefix(p, "-") {
		return ""
	}
	if strings.HasPrefix(p, "~") {
		return "使用 home 路径"
	}
	if strings.HasPrefix(p, "//") {
		return "使用 UNC 或网络绝对路径"
	}
	if strings.HasPrefix(p, "/") || windowsAbsPathPattern.MatchString(p) {
		return "使用绝对路径"
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "逃逸 background sandbox"
	}
	return ""
}

func appendUniqueReasons(base, extra []string) []string {
	out := append([]string{}, base...)
	for _, reason := range extra {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			continue
		}
		seen := false
		for _, existing := range out {
			if existing == reason {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, reason)
		}
	}
	return out
}
