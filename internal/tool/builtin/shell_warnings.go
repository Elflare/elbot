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

func analyzeBashShellAdvice(file *syntax.File, workDir string, fileGuard FileGuard) shellAdvice {
	var advice shellAdvice
	syntax.Walk(file, func(node syntax.Node) bool {
		if advice.blockErr != nil {
			return false
		}
		switch n := node.(type) {
		case *syntax.CallExpr:
			advice.inspectShellCall(n, workDir, fileGuard)
		case *syntax.Redirect:
			advice.inspectShellRedirect(n, workDir, fileGuard)
		}
		return true
	})
	return advice
}

func (a *shellAdvice) inspectShellCall(call *syntax.CallExpr, workDir string, fileGuard FileGuard) {
	if len(call.Args) == 0 {
		return
	}
	name, ok := literalWord(call.Args[0])
	if !ok {
		return
	}
	name = commandBase(name)
	args := literalArgs(call.Args[1:])

	// Protective checks are independent of command type: any literal argument
	// that resolves to a guarded path blocks the command, including reads.
	if a.checkProtectedPaths(fileGuard, workDir, literalArgs(call.Args)) {
		return
	}

	switch name {
	case "cat", "less", "more", "head", "tail", "grep", "rg":
		if a.addPathArgWarning(workDir, args) {
			a.addWarning(warnUseReadFile)
		}
	case "sed":
		// sed takes a script before optional file operands; require another
		// argument after it so a script alone is not mistaken for a file.
		if len(args) < 2 || !a.addPathArgWarning(workDir, args[1:]) {
			return
		}
		if hasArg(args, "-i") {
			a.addWarning(warnUseEditFile)
		} else {
			a.addWarning(warnUseReadFile)
		}
	case "perl":
		if hasArg(args, "-pi") || hasArg(args, "-pI") {
			if a.addPathArgWarning(workDir, args) {
				a.addWarning(warnUseEditFile)
			}
		}
	case "tee":
		if a.addPathArgWarning(workDir, args) {
			a.addWarning(warnUseEditFile)
		}
	}
}

func (a *shellAdvice) inspectShellRedirect(redir *syntax.Redirect, workDir string, fileGuard FileGuard) {
	op := redir.Op.String()
	if op == ">&" || op == "<&" {
		return
	}
	path, ok := literalWord(redir.Word)
	if !ok {
		return
	}
	resolved, ok := resolveShellLiteralPath(workDir, path)
	if !ok || resolved == os.DevNull {
		return
	}
	if err := fileGuard.CheckWrite(resolved); err != nil {
		a.blockErr = err
		return
	}
	if strings.Contains(op, ">") {
		a.addWarning(warnUseEditFile)
	} else if strings.Contains(op, "<") {
		a.addWarning(warnUseReadFile)
	}
}

func (a *shellAdvice) checkProtectedPaths(fileGuard FileGuard, workDir string, args []string) bool {
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

// addPathArgWarning treats literal non-option arguments as file candidates.
func (a *shellAdvice) addPathArgWarning(workDir string, args []string) bool {
	for _, arg := range args {
		if _, ok := resolveShellLiteralPath(workDir, arg); ok {
			a.addWarning(warnUseWorkspace)
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
	if raw == "" || strings.HasPrefix(raw, "-") || strings.Contains(raw, "://") {
		return "", false
	}
	if expanded, ok := msysPathToWindows(raw); ok {
		raw = expanded
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") || strings.HasPrefix(raw, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		raw = filepath.Join(home, raw[1:])
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
