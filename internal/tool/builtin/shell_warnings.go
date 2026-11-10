// 提取 Shell 执行相关警告，生成面向模型的提示。
package builtin

import (
	"os"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

type shellAdvice struct {
	warnings []string
	blockErr error
}

func analyzeBashShellAdvice(file *syntax.File, workDir string, fileGuard *FileGuard) shellAdvice {
	advice := &shellAdvice{}
	syntax.Walk(file, func(node syntax.Node) bool {
		if advice.blockErr != nil {
			return false
		}
		switch n := node.(type) {
		case nil:
			return true
		case *syntax.CallExpr:
			advice.inspectShellCall(n, workDir, fileGuard)
		case *syntax.Redirect:
			advice.inspectShellRedirect(n, workDir, fileGuard)
		}
		return true
	})
	return *advice
}

func (a *shellAdvice) inspectShellCall(call *syntax.CallExpr, workDir string, fileGuard *FileGuard) {
	if len(call.Args) == 0 {
		return
	}
	name, ok := literalWord(call.Args[0])
	if !ok {
		return
	}
	name = commandBase(name)
	args := literalArgs(call.Args[1:])
	switch name {
	case "cat", "less", "more", "head", "tail", "grep", "rg":
		a.addWarning(warnUseReadFile)
		a.addPathArgWarning(workDir, args)
		a.addReadWarnings(fileGuard, workDir, args)
	case "sed":
		// 先看作用目标：命中受管文件直接拦截，不区分是否原地编辑。
		if a.checkWriteArgs(fileGuard, workDir, args) {
			return
		}
		if hasArg(args, "-i") {
			a.addWarning(warnUseEditFile)
			a.addPathArgWarning(workDir, args)
			return
		}
		a.addWarning(warnUseReadFile)
		a.addPathArgWarning(workDir, args)
		a.addReadWarnings(fileGuard, workDir, args)
	case "perl":
		if hasArg(args, "-pi") || hasArg(args, "-pI") {
			if a.checkWriteArgs(fileGuard, workDir, args) {
				return
			}
			a.addWarning(warnUseEditFile)
			a.addPathArgWarning(workDir, args)
		}
	case "tee":
		if a.checkWriteArgs(fileGuard, workDir, args) {
			return
		}
		a.addWarning(warnUseEditFile)
		a.addPathArgWarning(workDir, args)
	}
}

// inspectShellRedirect 在重定向写入时给出提示；仅受管文件直接拒绝执行。
func (a *shellAdvice) inspectShellRedirect(redir *syntax.Redirect, workDir string, fileGuard *FileGuard) {
	// ">&"、"<&" 是 fd 复制；不含 ">" 的 heredoc、输入重定向都不写文件。
	if op := redir.Op.String(); op == ">&" || op == "<&" || !strings.Contains(op, ">") {
		return
	}
	path, ok := literalWord(redir.Word)
	if !ok {
		// 动态目标（如 mktemp 结果）无法确定归属，不判也不拦。
		return
	}
	resolved, hit := resolveShellLiteralPath(workDir, path)
	if !hit || resolved == os.DevNull {
		return
	}
	if err := fileGuard.CheckWrite(resolved); err != nil {
		a.blockErr = err
		return
	}
	a.addWarning(warnUseEditFile)
}

func (a *shellAdvice) addPathArgWarning(workDir string, args []string) {
	for _, arg := range args {
		if _, ok := resolveShellLiteralPath(workDir, arg); ok {
			a.addWarning(warnUseWorkspace)
			return
		}
	}
}

func (a *shellAdvice) addReadWarnings(fileGuard *FileGuard, workDir string, args []string) {
	for _, arg := range args {
		path, ok := resolveShellLiteralPath(workDir, arg)
		if !ok {
			continue
		}
		for _, warning := range fileGuard.ReadWarnings(path) {
			a.addWarning(warning)
		}
	}
}

func (a *shellAdvice) checkWriteArgs(fileGuard *FileGuard, workDir string, args []string) bool {
	for _, arg := range args {
		path, ok := resolveShellLiteralPath(workDir, arg)
		if !ok {
			continue
		}
		if err := fileGuard.CheckWrite(path); err != nil {
			a.blockErr = err
			return true
		}
	}
	return false
}

func (a *shellAdvice) addWarning(warning string) {
	warning = strings.TrimSpace(warning)
	if warning == "" {
		return
	}
	for _, existing := range a.warnings {
		if existing == warning {
			return
		}
	}
	a.warnings = append(a.warnings, warning)
}

func resolveShellLiteralPath(workDir, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "-") {
		return "", false
	}
	if strings.Contains(raw, "://") {
		return "", false
	}
	if expanded, ok := msysPathToWindows(raw); ok {
		raw = expanded
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw), true
	}
	if strings.TrimSpace(workDir) == "" {
		return "", false
	}
	return filepath.Clean(filepath.Join(workDir, raw)), true
}

func msysPathToWindows(path string) (string, bool) {
	if len(path) < 3 || path[0] != '/' || path[2] != '/' {
		return "", false
	}
	drive := path[1]
	if !((drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z')) {
		return "", false
	}
	return string(drive) + ":/" + path[3:], true
}
