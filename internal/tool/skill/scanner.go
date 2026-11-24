// 扫描 Skill 文件并生成候选记录，不修改注册目录、活动目录或技能文件。
package skill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"elbot/internal/elyph"
	toolshare "elbot/internal/tool/share"
)

type filesystemScanner struct {
	root string // 管理器传入的技能资产目录，每个直接子目录是一个 Skill。
}

func (s filesystemScanner) scanRecords(ctx context.Context) ([]record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := s.root
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skill dir %q: %w", dir, err)
	}
	records := []record{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(dir, entry.Name())
		record, ok, err := s.readRecord(root, entry.Name())
		if err != nil {
			return nil, err
		}
		if ok {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	return records, nil
}

func (s filesystemScanner) readRecord(root, dirName string) (record, bool, error) {
	if data, err := os.ReadFile(filepath.Join(root, elyph.SkillFileName)); err == nil {
		doc, err := elyph.ParseSkill(string(data), dirName)
		if err != nil {
			return record{}, false, fmt.Errorf("parse %s in %q: %w", elyph.SkillFileName, root, err)
		}
		detail := strings.TrimSpace(string(data))
		record := record{Name: doc.Name, Description: doc.Description, Detail: detail, Format: elyph.Format, Risk: toolshare.RiskSafe, Root: root}
		return s.withAgentManifest(record)
	} else if !os.IsNotExist(err) {
		return record{}, false, fmt.Errorf("read %s in %q: %w", elyph.SkillFileName, root, err)
	}
	detailPath := filepath.Join(root, "SKILL.md")
	data, err := os.ReadFile(detailPath)
	if os.IsNotExist(err) {
		return record{}, false, nil
	}
	if err != nil {
		return record{}, false, fmt.Errorf("read SKILL.md in %q: %w", root, err)
	}
	def, err := ParseSkillMarkdown(data, dirName)
	if err != nil {
		return record{}, false, fmt.Errorf("parse SKILL.md in %q: %w", root, err)
	}
	record := record{Name: strings.Clone(def.Name), Description: strings.Clone(def.Description), DetailPath: detailPath, Format: def.Format, Risk: toolshare.RiskSafe, Root: root}
	return s.withAgentManifest(record)
}

func (s filesystemScanner) withAgentManifest(record record) (record, bool, error) {
	manifest, found, err := LoadAgentSkillManifest(record.Root)
	record.ManifestFound = found
	if err != nil {
		return record, false, fmt.Errorf("skill %q: %w", record.Name, err)
	}
	if found {
		record.Manifest = manifest
		record.Risk = manifest.Risk
		record.SuperadminOnly = manifest.SuperadminOnly
		record.Tags = manifest.Tags
	}
	return record, true, nil
}
