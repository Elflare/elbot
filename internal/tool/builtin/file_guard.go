package builtin

import (
	"fmt"
	"path/filepath"
	"strings"

	"elbot/internal/elyph"
)

const (
	warnUseReadFile         = "读取本地文件请优先使用 read_file 工具。"
	warnUseEditFile         = "编辑本地文件请优先使用 edit_file 工具。"
	warnUseReadElSkill      = "读取 EL Skill 文件请使用 read_el_skill。"
	warnUseModifyElSkill    = "EL Skill 文件请使用 modify_el_skill 修改。"
	warnUseReadMemory       = "读取常驻记忆请使用 resident_memory_read。"
	warnUseModifyMemory     = "常驻记忆文件请使用 resident_memory_normal 或 resident_memory_core 修改。"
	warnUseReadLongMemory   = "读取长期记忆请使用 long_memory_search。"
	warnUseModifyLongMemory = "长期记忆文件请使用 long_memory_write 修改。"
)

type FileGuard struct {
	rules []FileGuardRule
}

type FileGuardRule struct {
	Name         string
	Root         string
	ExactPath    string
	ReadWarnings []string
	WriteError   string
	Match        func(absPath, rel string) bool
}

func NewFileGuard(rules ...FileGuardRule) *FileGuard {
	guard := &FileGuard{}
	for _, rule := range rules {
		guard.AddRule(rule)
	}
	return guard
}

func (g *FileGuard) AddRule(rule FileGuardRule) {
	if g == nil || strings.TrimSpace(rule.Root) == "" && strings.TrimSpace(rule.ExactPath) == "" {
		return
	}
	g.rules = append(g.rules, rule)
}

func (g *FileGuard) ReadWarnings(path string) []string {
	if g == nil {
		return nil
	}
	warnings := []string{}
	for _, rule := range g.rules {
		if rule.matches(path) {
			warnings = append(warnings, rule.ReadWarnings...)
		}
	}
	return warnings
}

func (g *FileGuard) CheckWrite(path string) error {
	if g == nil {
		return nil
	}
	for _, rule := range g.rules {
		if !rule.matches(path) || strings.TrimSpace(rule.WriteError) == "" {
			continue
		}
		return fmt.Errorf("%s", strings.TrimSpace(rule.WriteError))
	}
	return nil
}

func (r FileGuardRule) matches(path string) bool {
	path, err := absClean(path)
	if err != nil {
		return false
	}
	if exact := strings.TrimSpace(r.ExactPath); exact != "" {
		exact, err = absClean(exact)
		if err != nil {
			return false
		}
		return samePath(path, exact)
	}
	root := strings.TrimSpace(r.Root)
	if root == "" {
		return false
	}
	root, err = absClean(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return false
	}
	if r.Match == nil {
		return true
	}
	return r.Match(path, filepath.ToSlash(rel))
}

func NewElSkillFileGuardRule(skillRoot string) FileGuardRule {
	return FileGuardRule{
		Name:         "el_skill",
		Root:         skillRoot,
		ReadWarnings: []string{warnUseReadElSkill},
		WriteError:   warnUseModifyElSkill,
		Match: func(absPath, rel string) bool {
			parts := strings.Split(filepath.ToSlash(rel), "/")
			if len(parts) != 3 || parts[0] != "go" || strings.TrimSpace(parts[1]) == "" {
				return false
			}
			return parts[2] == elyph.SkillFileName || parts[2] == "main.go"
		},
	}
}

func NewResidentMemoryFileGuardRule(path string) FileGuardRule {
	return FileGuardRule{
		Name:         "resident_memory",
		ExactPath:    path,
		ReadWarnings: []string{warnUseReadMemory},
		WriteError:   warnUseModifyMemory,
	}
}

func NewLongMemoryFileGuardRule(rootDir string) FileGuardRule {
	return FileGuardRule{
		Name:         "long_memory",
		Root:         filepath.Join(rootDir, "memories"),
		ReadWarnings: []string{warnUseReadLongMemory},
		WriteError:   warnUseModifyLongMemory,
		Match: func(absPath, rel string) bool {
			return strings.EqualFold(filepath.Ext(rel), ".md")
		},
	}
}

func firstFileGuard(values []*FileGuard) *FileGuard {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func absClean(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func samePath(a, b string) bool {
	if a == b {
		return true
	}
	return strings.EqualFold(filepath.ToSlash(a), filepath.ToSlash(b))
}
