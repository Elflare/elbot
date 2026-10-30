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
		case nil:
			return true
		case *syntax.CallExpr:
			c.classifyCall(n)
		case *syntax.Redirect:
			c.classifyRedirect(n)
		case *syntax.CmdSubst:
			c.raise(toolshare.RiskHigh, "包含命令替换 $() 或反引号，执行内容更难静态判断")
		}
		return true
	})
	c.classifyPipeline()
	if len(c.reasons) == 0 {
		c.reasons = append(c.reasons, "未发现明显写入、删除、提权或下载即执行行为")
	}
	return toolshare.RiskAssessment{Level: c.level, Reasons: c.reasons}
}

func (c *shellRiskClassifier) classifyCall(call *syntax.CallExpr) {
	if len(call.Args) == 0 {
		return
	}
	name, ok := literalWord(call.Args[0])
	if !ok || name == "" {
		c.raise(toolshare.RiskHigh, "命令名包含变量、命令替换或其他动态结构，无法静态判断真实命令")
		return
	}
	name = commandBase(name)
	args := literalArgs(call.Args[1:])
	c.pipeline = append(c.pipeline, name)

	switch name {
	case "rm", "rmdir", "del", "erase", "rd":
		if hasRecursiveForce(args) || touchesSystemPath(args) {
			c.raise(toolshare.RiskCritical, "使用删除命令 "+name+" 且包含递归/强制参数或系统路径")
		} else {
			c.raise(toolshare.RiskHigh, "使用删除命令 "+name)
		}
	case "sudo", "su", "doas", "runas":
		c.raise(toolshare.RiskCritical, "使用提权命令 "+name)
	case "mkfs", "fdisk", "parted", "diskpart", "format", "mount", "umount", "dd":
		c.raise(toolshare.RiskCritical, "使用磁盘或系统级操作命令 "+name)
	case "chmod", "chown", "icacls", "takeown":
		if hasRecursive(args) || touchesSystemPath(args) {
			c.raise(toolshare.RiskCritical, "递归或系统路径权限修改命令 "+name)
		} else {
			c.raise(toolshare.RiskHigh, "权限或所有者修改命令 "+name)
		}
	case "curl", "wget":
		c.raise(toolshare.RiskMedium, "使用网络下载命令 "+name)
	case "git":
		c.classifyGit(args)
	case "sh", "bash", "zsh", "fish", "python", "python3", "node", "perl", "ruby", "powershell", "pwsh":
		c.raise(toolshare.RiskMedium, "调用解释器或 shell "+name)
	case "sed":
		if hasArg(args, "-i") {
			c.raise(toolshare.RiskHigh, "sed -i 会原地修改文件")
		}
	case "mv", "move", "cp", "copy", "install", "tee":
		c.raise(toolshare.RiskHigh, "可能写入或覆盖文件的命令 "+name)
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
		if sub == "reset" && hasArg(args[1:], "--hard") || sub == "clean" {
			c.raise(toolshare.RiskHigh, "git "+sub+" 可能丢弃或删除工作区内容")
			return
		}
		c.raise(toolshare.RiskHigh, "git "+sub+" 会修改仓库状态")
	}
}

func (c *shellRiskClassifier) classifyPackageOrBuild(name string, args []string) {
	if len(args) == 0 {
		return
	}
	sub := args[0]
	switch name {
	case "go":
		if sub == "test" || sub == "build" || sub == "run" {
			c.raise(toolshare.RiskMedium, "go "+sub+" 可能执行项目代码")
		} else if sub == "install" || sub == "get" {
			c.raise(toolshare.RiskHigh, "go "+sub+" 会安装或修改依赖")
		}
	case "npm", "pnpm", "yarn", "pip", "pip3", "cargo":
		if sub == "install" || sub == "add" || sub == "remove" || sub == "update" {
			c.raise(toolshare.RiskHigh, name+" "+sub+" 会修改依赖或安装代码")
		} else if sub == "test" || sub == "run" || sub == "build" {
			c.raise(toolshare.RiskMedium, name+" "+sub+" 可能执行项目脚本")
		}
	}
}

func (c *shellRiskClassifier) classifyRedirect(redir *syntax.Redirect) {
	op := redir.Op.String()
	if strings.Contains(op, ">") || strings.Contains(op, "<>") {
		c.raise(toolshare.RiskHigh, "包含写入重定向 "+op)
	}
}

func (c *shellRiskClassifier) classifyPipeline() {
	if len(c.pipeline) < 2 {
		return
	}
	for i := 0; i < len(c.pipeline)-1; i++ {
		left, right := c.pipeline[i], c.pipeline[i+1]
		if isDownloader(left) && isInterpreter(right) {
			c.raise(toolshare.RiskCritical, "网络下载命令 "+left+" 的输出通过管道交给 "+right+" 执行")
		} else if isInterpreter(right) {
			c.raise(toolshare.RiskHigh, "管道输出交给解释器或 shell "+right+" 执行")
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
		if arg == "-r" || arg == "-R" || arg == "--recursive" || strings.Contains(arg, "r") && strings.HasPrefix(arg, "-") {
			return true
		}
	}
	return false
}

func hasRecursiveForce(args []string) bool {
	var recursive, force bool
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			recursive = recursive || strings.Contains(arg, "r") || strings.Contains(arg, "R") || arg == "--recursive"
			force = force || strings.Contains(arg, "f") || arg == "--force"
		}
	}
	return recursive && force
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
