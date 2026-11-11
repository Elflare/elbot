// 评估 Shell 命令风险，给出确认所需的等级与原因。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"strings"

	"elbot/internal/security"

	"mvdan.cc/sh/v3/syntax"
)

type shellRiskClassifier struct {
	level    toolshare.RiskLevel
	reasons  []string
	pipeline []string
}

func classifyBashShellCommand(file *syntax.File) toolshare.RiskAssessment {
	c := &shellRiskClassifier{level: toolshare.RiskLow}
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.CallExpr:
			c.classifyCall(n)
		case *syntax.Redirect:
			c.classifyRedirect(n)
		case *syntax.CmdSubst:
			c.raise(toolshare.RiskHigh, "$()")
		}
		return true
	})
	c.classifyPipeline()
	return toolshare.RiskAssessment{Level: c.level, Reasons: c.reasons}
}

func (c *shellRiskClassifier) classifyCall(call *syntax.CallExpr) {
	if len(call.Args) == 0 {
		return
	}
	name, ok := literalWord(call.Args[0])
	if !ok || name == "" {
		c.raise(toolshare.RiskHigh, "unk")
		return
	}
	name = commandBase(name)
	args := literalArgs(call.Args[1:])
	c.pipeline = append(c.pipeline, name)

	switch name {
	case "rm", "rmdir", "del", "erase", "rd", "chmod", "chown", "icacls", "takeown":
		if hasRecursive(args) || touchesSystemPath(args) {
			c.raise(toolshare.RiskCritical, name)
		} else {
			c.raise(toolshare.RiskHigh, name)
		}
	case "sudo", "su", "doas", "runas":
		c.raise(toolshare.RiskCritical, name)
	case "mkfs", "fdisk", "parted", "diskpart", "format", "mount", "umount", "dd":
		c.raise(toolshare.RiskCritical, name)
	case "curl", "wget":
		c.raise(toolshare.RiskMedium, name)
	case "git":
		c.classifyGit(args)
	case "sh", "bash", "zsh", "fish", "python", "python3", "node", "perl", "ruby", "powershell", "pwsh":
		c.raise(toolshare.RiskMedium, name)
	case "sed":
		if hasArg(args, "-i") {
			c.raise(toolshare.RiskHigh, name)
		}
	case "mv", "move", "cp", "copy", "install", "tee":
		c.raise(toolshare.RiskHigh, name)
	case "npm", "pnpm", "yarn", "pip", "pip3", "go", "cargo":
		c.classifyPackageOrBuild(name, args)
	}
}

func (c *shellRiskClassifier) classifyGit(args []string) {
	if len(args) == 0 {
		return
	}
	sub := args[0]
	switch sub {
	case "status", "diff", "log", "show", "branch":
		return
	case "reset", "clean", "checkout", "switch", "restore", "commit", "push", "add", "merge", "rebase", "cherry-pick", "stash":
		c.raise(toolshare.RiskHigh, "git "+sub)
	}
}

func (c *shellRiskClassifier) classifyPackageOrBuild(name string, args []string) {
	if len(args) == 0 {
		return
	}
	sub := args[0]
	switch sub {
	case "install", "add", "remove", "update":
		c.raise(toolshare.RiskHigh, name+" "+sub)
	case "get":
		if name == "go" {
			c.raise(toolshare.RiskHigh, name+" "+sub)
		}
	case "test", "build", "run":
		c.raise(toolshare.RiskMedium, name+" "+sub)
	}
}

func (c *shellRiskClassifier) classifyRedirect(redir *syntax.Redirect) {
	op := redir.Op.String()
	if strings.Contains(op, ">") || strings.Contains(op, "<>") {
		c.raise(toolshare.RiskHigh, op)
	}
}

func (c *shellRiskClassifier) classifyPipeline() {
	if len(c.pipeline) < 2 {
		return
	}
	for i := 0; i < len(c.pipeline)-1; i++ {
		left, right := c.pipeline[i], c.pipeline[i+1]
		if isDownloader(left) && isInterpreter(right) {
			c.raise(toolshare.RiskCritical, left+" | "+right)
		} else if isInterpreter(right) {
			c.raise(toolshare.RiskHigh, right)
		}
	}
}

func (c *shellRiskClassifier) raise(level toolshare.RiskLevel, reason string) {
	if security.CompareRisk(level, c.level) > 0 {
		c.level = level
	}
	for _, existing := range c.reasons {
		if existing == reason {
			return
		}
	}
	c.reasons = append(c.reasons, reason)
}

func literalArgs(words []*syntax.Word) []string {
	args := make([]string, 0, len(words))
	for _, word := range words {
		if arg, ok := literalWord(word); ok {
			args = append(args, arg)
		}
	}
	return args
}

func literalWord(word *syntax.Word) (string, bool) {
	if word == nil || len(word.Parts) == 0 {
		return "", false
	}
	var sb strings.Builder
	for _, part := range word.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			sb.WriteString(p.Value)
		case *syntax.SglQuoted:
			sb.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, quoted := range p.Parts {
				lit, ok := quoted.(*syntax.Lit)
				if !ok {
					return "", false
				}
				sb.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}
	return sb.String(), true
}

func commandBase(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "\\", "/")
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	return strings.TrimSuffix(name, ".exe")
}

func hasArg(args []string, target string) bool {
	for _, arg := range args {
		if arg == target || strings.Contains(arg, target) {
			return true
		}
	}
	return false
}

func hasRecursive(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") && (strings.Contains(arg, "r") || strings.Contains(arg, "R")) {
			return true
		}
	}
	return false
}

func touchesSystemPath(args []string) bool {
	for _, arg := range args {
		path := strings.ToLower(strings.Trim(strings.ReplaceAll(arg, "\\", "/"), "'\""))
		switch path {
		case "/", "/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/lib64", "/proc", "/root", "/sbin", "/sys", "/usr", "/var", "c:/", "c:/windows", "c:/program files", "c:/users":
			return true
		}
		if strings.HasPrefix(path, "/etc/") || strings.HasPrefix(path, "/usr/") || strings.HasPrefix(path, "c:/windows/") {
			return true
		}
	}
	return false
}

func isDownloader(name string) bool {
	return name == "curl" || name == "wget"
}

func isInterpreter(name string) bool {
	switch name {
	case "sh", "bash", "zsh", "fish", "python", "python3", "node", "perl", "ruby", "powershell", "pwsh":
		return true
	default:
		return false
	}
}
