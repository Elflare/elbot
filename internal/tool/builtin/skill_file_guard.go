package builtin

import (
	"fmt"
	"path/filepath"
	"strings"

	"elbot/internal/elyph"
)

const (
	warnUseReadFile      = "读取本地文件请优先使用 read_file 工具。"
	warnUseEditFile      = "编辑本地文件请优先使用 edit_file 工具。"
	warnUseReadElSkill   = "读取 EL Skill 文件请使用 read_el_skill。"
	warnUseModifyElSkill = "修改 EL Skill 文件请使用 modify_el_skill。"
)

type elSkillFile struct {
	Name   string
	Target string
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func detectElSkillFile(skillRoot, path string) (elSkillFile, bool) {
	skillRoot = strings.TrimSpace(skillRoot)
	path = strings.TrimSpace(path)
	if skillRoot == "" || path == "" {
		return elSkillFile{}, false
	}
	root, err := filepath.Abs(skillRoot)
	if err != nil {
		return elSkillFile{}, false
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return elSkillFile{}, false
	}
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return elSkillFile{}, false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 3 || parts[0] != "go" || strings.TrimSpace(parts[1]) == "" {
		return elSkillFile{}, false
	}
	switch parts[2] {
	case elyph.SkillFileName:
		return elSkillFile{Name: parts[1], Target: "skill_elyph"}, true
	case "main.go":
		return elSkillFile{Name: parts[1], Target: "code_source"}, true
	default:
		return elSkillFile{}, false
	}
}

func rejectElSkillFileEdit(skillRoot, path string) error {
	file, ok := detectElSkillFile(skillRoot, path)
	if !ok {
		return nil
	}
	return fmt.Errorf("EL Skill %s target %s must be modified with modify_el_skill", file.Name, file.Target)
}

func readFileWarnings(skillRoot, path string) []string {
	if _, ok := detectElSkillFile(skillRoot, path); ok {
		return []string{warnUseReadElSkill}
	}
	return nil
}
