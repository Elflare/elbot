package builtin

import (
	"fmt"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

type shellAdvice struct {
	warnings []string
	blockErr error
}

func analyzeShellAdvice(cmdText, workDir, skillRoot string) shellAdvice {
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(cmdText), "")
	if err != nil {
		return shellAdvice{}
	}
	advice := &shellAdvice{}
	syntax.Walk(file, func(node syntax.Node) bool {
		if advice.blockErr != nil {
			return false
		}
		switch n := node.(type) {
		case nil:
			return true
		case *syntax.CallExpr:
			advice.inspectShellCall(n, workDir, skillRoot)
		case *syntax.Redirect:
			advice.inspectShellRedirect(n, workDir, skillRoot)
		}
		return true
	})
	return *advice
}

func (a *shellAdvice) inspectShellCall(call *syntax.CallExpr, workDir, skillRoot string) {
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
		if hasElSkillPathArg(skillRoot, workDir, args) {
			a.addWarning(warnUseReadElSkill)
		}
	case "sed":
		if hasArg(args, "-i") {
			if path, ok := firstElSkillPathArg(skillRoot, workDir, args); ok {
				a.blockErr = fmt.Errorf("EL Skill file %s must be modified with modify_el_skill", path)
				return
			}
			a.addWarning(warnUseEditFile)
		}
	case "perl":
		if hasArg(args, "-pi") || hasArg(args, "-pI") {
			if path, ok := firstElSkillPathArg(skillRoot, workDir, args); ok {
				a.blockErr = fmt.Errorf("EL Skill file %s must be modified with modify_el_skill", path)
				return
			}
			a.addWarning(warnUseEditFile)
		}
	case "tee":
		if path, ok := firstElSkillPathArg(skillRoot, workDir, args); ok {
			a.blockErr = fmt.Errorf("EL Skill file %s must be modified with modify_el_skill", path)
			return
		}
		a.addWarning(warnUseEditFile)
	}
}

func (a *shellAdvice) inspectShellRedirect(redir *syntax.Redirect, workDir, skillRoot string) {
	op := redir.Op.String()
	if !strings.Contains(op, ">") && !strings.Contains(op, "<>") {
		return
	}
	path, ok := literalWord(redir.Word)
	if ok {
		if resolved, hit := resolveElSkillShellPath(skillRoot, workDir, path); hit {
			a.blockErr = fmt.Errorf("EL Skill file %s must be modified with modify_el_skill", resolved)
			return
		}
	}
	a.addWarning(warnUseEditFile)
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

func hasElSkillPathArg(skillRoot, workDir string, args []string) bool {
	_, ok := firstElSkillPathArg(skillRoot, workDir, args)
	return ok
}

func firstElSkillPathArg(skillRoot, workDir string, args []string) (string, bool) {
	for _, arg := range args {
		path, ok := resolveElSkillShellPath(skillRoot, workDir, arg)
		if ok {
			return path, true
		}
	}
	return "", false
}

func resolveElSkillShellPath(skillRoot, workDir, raw string) (string, bool) {
	path, ok := resolveShellLiteralPath(workDir, raw)
	if !ok {
		return "", false
	}
	if _, ok := detectElSkillFile(skillRoot, path); ok {
		return path, true
	}
	return "", false
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
