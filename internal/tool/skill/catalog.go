package skill

import (
	"path/filepath"
	"sort"
	"sync"

	"elbot/internal/tool"
)

type Kind string

const (
	KindAgent Kind = "agent"
	KindGo    Kind = "go"
)

type Record struct {
	Name        string
	Description string
	Detail      string
	Format      string
	Risk        tool.RiskLevel
	Kind        Kind
	Root        string
	BinaryPath  string
}

type Catalog struct {
	mu      sync.RWMutex
	records map[string]Record
}

func NewCatalog() *Catalog {
	return &Catalog{records: map[string]Record{}}
}

func (c *Catalog) Replace(records []Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = map[string]Record{}
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

func (c *Catalog) Get(name string) (Record, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	record, ok := c.records[name]
	return record, ok
}

func (c *Catalog) List() []Record {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Record, 0, len(c.records))
	for _, record := range c.records {
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func SourceForKind(kind Kind) tool.Source {
	if kind == KindGo {
		return tool.SourceSkillGo
	}
	return tool.SourceSkillAgent
}
