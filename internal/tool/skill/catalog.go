// 维护 Skill 目录与查询结果。
package skill

import (
	"path/filepath"
	"sort"
	"sync"

	toolshare "elbot/internal/tool/share"
)

type Kind string

const (
	KindAgent Kind = "agent"
	KindGo    Kind = "go"
)

// record 保存一次扫描得到的技能事实，发布后只读。
type record struct {
	Name           string              // 注册名称。
	Description    string              // 发现与工具声明使用的简短说明。
	Detail         string              // 扫描时读取的 ELyph 正文。
	DetailPath     string              // 按需读取的 SKILL.md 路径，不缓存其正文。
	Format         string              // 正文的展示格式。
	Risk           toolshare.RiskLevel // 技能声明的风险级别。
	SuperadminOnly bool                // 是否仅允许超级管理员访问。
	Tags           []string            // 发现与预加载使用的技能标签。
	Kind           Kind                // Agent 文档技能或原生 Go 技能。
	Root           string              // 技能自身的资产目录与执行工作目录。
	BinaryPath     string              // Go 技能可执行文件路径；纯文档技能为空。
	Manifest       AgentSkillManifest  // 已解析的 ELBOT_SKILL.toml。
	ManifestFound  bool                // 是否存在 ELBOT_SKILL.toml。
	ManifestError  string              // 清单读取或解析错误，供发现时展示。
}

type catalog struct {
	mu      sync.RWMutex      // 保护活动目录替换和查询。
	records map[string]record // 已成功注册的技能快照，按名称索引。
}

func newCatalog() *catalog {
	return &catalog{records: map[string]record{}}
}

func (c *catalog) Replace(records []record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = map[string]record{}
	for _, record := range records {
		if record.Name == "" {
			continue
		}
		if abs, err := filepath.Abs(record.Root); err == nil {
			record.Root = abs
		} else {
			record.Root = filepath.Clean(record.Root)
		}
		if record.BinaryPath != "" {
			if abs, err := filepath.Abs(record.BinaryPath); err == nil {
				record.BinaryPath = abs
			} else {
				record.BinaryPath = filepath.Clean(record.BinaryPath)
			}
		}
		c.records[record.Name] = record
	}
}

func (c *catalog) Get(name string) (record, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	record, ok := c.records[name]
	return record, ok
}

func (c *catalog) List() []record {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]record, 0, len(c.records))
	for _, record := range c.records {
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func SourceForKind(kind Kind) toolshare.Source {
	if kind == KindGo {
		return toolshare.SourceSkillGo
	}
	return toolshare.SourceSkillAgent
}
