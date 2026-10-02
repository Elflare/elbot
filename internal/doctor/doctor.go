// Package doctor inspects configuration assets without changing files or loading runtimes.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"elbot/internal/config"
	"github.com/pelletier/go-toml/v2"
)

type FileIssue struct {
	Path     string
	Problems []string
}

type Report struct {
	ConfigPath string
	Files      []FileIssue
}

func (r *Report) add(path, problem string) {
	for i := range r.Files {
		if r.Files[i].Path == path {
			if !slices.Contains(r.Files[i].Problems, problem) {
				r.Files[i].Problems = append(r.Files[i].Problems, problem)
			}
			return
		}
	}
	r.Files = append(r.Files, FileIssue{Path: path, Problems: []string{problem}})
}

// Text renders the diagnosis as a request the user can copy to Elbot.
func (r Report) Text() string {
	if len(r.Files) == 0 {
		return "Everything is OK"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "发现 %d 个文件需要检查。请复制以下内容给 Elbot：\n\n", len(r.Files))
	out.WriteString("请参考以下资料，处理列出的 ElBot 配置问题：\n")
	out.WriteString("配置说明：https://raw.githubusercontent.com/Elflare/elbot/main/docs/configuration.md\n")
	out.WriteString("默认模板：https://raw.githubusercontent.com/Elflare/elbot/main/internal/config/assets.go\n\n")
	fmt.Fprintf(&out, "主配置：%s\n\n检查结果：\n", r.ConfigPath)
	for i, file := range r.Files {
		if i > 0 {
			out.WriteByte('\n')
		}
		fmt.Fprintf(&out, "%d. %s\n", i+1, file.Path)
		for _, problem := range file.Problems {
			fmt.Fprintf(&out, "   - %s\n", problem)
		}
	}
	out.WriteString("\n修改前请备份；补齐缺失配置及对应注释，保留已有配置值和注释；格式错误、重复定义和未知节点先说明处理建议，Skill 差异只说明、不覆盖。")
	return out.String()
}

type Service struct {
	configPath string
}

// New binds inspection to the running service's configuration, not a default path.
func New(configPath string) (*Service, error) {
	if strings.TrimSpace(configPath) == "" {
		return nil, fmt.Errorf("doctor: main config path is required")
	}
	path, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("doctor: resolve config path: %w", err)
	}
	return &Service{configPath: path}, nil
}

var configAssetOrder = []string{
	"app.toml", "providers.toml", "state.toml", "elnis.toml", "tool_tags.toml", "plugins/hooks.toml",
}

// Check reads fresh disk content on each invocation. Errors in individual files
// belong in the report; cancellation and invalid built-in templates are errors.
func (s *Service) Check(ctx context.Context) (Report, error) {
	report := Report{ConfigPath: s.configPath}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	assets := config.DefaultAssets()
	byName := make(map[string]config.DefaultAsset, len(assets))
	for _, asset := range assets {
		byName[filepath.ToSlash(asset.Path)] = asset
	}
	var mainDefaults map[string]any
	if err := toml.Unmarshal([]byte(byName["app.toml"].Content), &mainDefaults); err != nil {
		return report, fmt.Errorf("doctor: parse bundled app config: %w", err)
	}
	mainData, err := os.ReadFile(s.configPath)
	var main map[string]any
	if err != nil {
		report.add(s.configPath, readProblem(err))
		if errors.Is(err, os.ErrNotExist) {
			main = mainDefaults
		}
	} else {
		main, err = inspectConfig(&report, s.configPath, "app.toml", mainData, byName["app.toml"].Content)
		if err != nil {
			return report, err
		}
	}
	paths := s.assetPaths(&report, main, mainDefaults)

	// Configuration first, then other assets, then built-in skills. Asset order is
	// retained within each group, so repeated checks produce stable reports.
	var ordered []config.DefaultAsset
	for _, name := range configAssetOrder[1:] {
		ordered = append(ordered, byName[name])
	}
	for _, skills := range []bool{false, true} {
		for _, asset := range assets {
			name := filepath.ToSlash(asset.Path)
			if slices.Contains(configAssetOrder, name) || isSkill(name) != skills {
				continue
			}
			ordered = append(ordered, asset)
		}
	}
	for _, asset := range ordered {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		name := filepath.ToSlash(asset.Path)
		path, special := paths[name]
		if special && path == "" {
			continue // Its location could not be resolved from the main config.
		}
		if !special {
			path = filepath.Join(filepath.Dir(s.configPath), asset.Path)
		}
		if !slices.Contains(configAssetOrder, name) && !isSkill(name) {
			info, err := os.Stat(path)
			if err != nil {
				report.add(path, readProblem(err))
			} else if !info.Mode().IsRegular() {
				report.add(path, "路径不是普通文件。")
			}
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			report.add(path, readProblem(err))
			continue
		}
		if isSkill(name) {
			if filepath.Ext(name) == ".toml" {
				parseTOML(&report, path, data)
			}
			if strings.ReplaceAll(string(data), "\r\n", "\n") != strings.ReplaceAll(asset.Content, "\r\n", "\n") {
				report.add(path, "内容与当前内置版本不同，需要查看差异。")
			}
			continue
		}
		if _, err := inspectConfig(&report, path, name, data, asset.Content); err != nil {
			return report, err
		}
	}
	return report, nil
}

func isSkill(name string) bool { return strings.HasPrefix(name, "skills/agent/") }

func readProblem(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "文件缺失。"
	}
	return "无法读取文件：" + err.Error()
}

func (s *Service) assetPaths(report *Report, main, defaults map[string]any) map[string]string {
	paths := make(map[string]string)
	refs := []struct{ asset, table, field string }{
		{"providers.toml", "config_files", "providers"},
		{"state.toml", "config_files", "state"},
		{"elnis.toml", "config_files", "elnis"},
		{"tool_tags.toml", "config_files", "tool_tags"},
		{"SOUL.md", "soul", "path"},
	}
	if main == nil {
		report.add(s.configPath, "主配置无法读取或解析，未检查依赖 config_files 和 soul.path 定位的文件。")
	}
	for _, ref := range refs {
		paths[ref.asset] = ""
		if main == nil {
			continue
		}
		fallback := defaults[ref.table].(map[string]any)[ref.field].(string)
		value := fallback
		if raw, exists := main[ref.table]; exists {
			table, ok := raw.(map[string]any)
			if !ok {
				report.add(s.configPath, "无法定位 "+ref.asset+"："+ref.table+" 必须是表。")
				continue
			}
			if raw, exists := table[ref.field]; exists {
				var ok bool
				value, ok = raw.(string)
				if !ok {
					report.add(s.configPath, "无法定位 "+ref.asset+"："+ref.table+"."+ref.field+" 必须是字符串。")
					continue
				}
				if value == "" {
					value = fallback // Matches config.applyAppDefaults.
				}
			}
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(filepath.Dir(s.configPath), value)
		}
		paths[ref.asset] = filepath.Clean(value)
	}
	return paths
}
