package skill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"elbot/internal/elyph"
	"elbot/internal/tool"
)

const windowsAppDirName = "ElBot"
const xdgAppDirName = "elbot"

type Scanner interface {
	Scan(ctx context.Context) ([]tool.Tool, error)
	Reload(ctx context.Context, registry *tool.Registry) error
}

type FilesystemScanner struct {
	Root    string
	Catalog *Catalog
}

func NewFilesystemScanner(root string) FilesystemScanner {
	if root == "" {
		root = DefaultRoot()
	}
	return FilesystemScanner{Root: root, Catalog: NewCatalog()}
}

func DefaultRoot() string {
	if runtime.GOOS == "windows" {
		if dir, err := os.UserConfigDir(); err == nil && strings.TrimSpace(dir) != "" {
			return filepath.Join(dir, windowsAppDirName, "skills")
		}
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dir != "" {
		return filepath.Join(dir, xdgAppDirName, "skills")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".local", "share", xdgAppDirName, "skills")
	}
	return "skills"
}

func (s FilesystemScanner) Scan(ctx context.Context) ([]tool.Tool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records, err := s.scanRecords(ctx)
	if err != nil {
		return nil, err
	}
	tools := make([]tool.Tool, 0, len(records))
	for _, record := range records {
		tools = append(tools, NewDescriptor(record))
	}
	return tools, nil
}

func (s FilesystemScanner) Reload(ctx context.Context, registry *tool.Registry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if registry == nil {
		return nil
	}
	records, err := s.scanRecords(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, record := range records {
		seen[record.Name] = true
	}
	for _, info := range registry.List() {
		if (info.Source == tool.SourceSkillAgent || info.Source == tool.SourceSkillGo) && !seen[info.Name] {
			_ = registry.Unregister(info.Name)
		}
	}
	registered := make([]Record, 0, len(records))
	for _, record := range records {
		if existing, ok := registry.Get(record.Name); ok {
			if existing.Info().Source == tool.SourceSkillAgent || existing.Info().Source == tool.SourceSkillGo {
				_ = registry.Unregister(record.Name)
			} else {
				continue
			}
		}
		if err := registry.Register(NewDescriptor(record)); err != nil {
			continue
		}
		registered = append(registered, record)
	}
	if s.Catalog != nil {
		s.Catalog.Replace(registered)
	}
	return nil
}

func (s FilesystemScanner) Remove(ctx context.Context, registry *tool.Registry, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.Catalog == nil {
		return fmt.Errorf("skill catalog is not configured")
	}
	record, ok := s.Catalog.Get(name)
	if !ok {
		return fmt.Errorf("external skill %q not found", name)
	}
	if record.Root == "" {
		return fmt.Errorf("external skill %q has no root directory", name)
	}
	if err := os.RemoveAll(record.Root); err != nil {
		return fmt.Errorf("remove skill directory %q: %w", record.Root, err)
	}
	return s.Reload(ctx, registry)
}

func (s FilesystemScanner) scanRecords(ctx context.Context) ([]Record, error) {
	records := []Record{}
	agentRecords, err := s.scanKind(ctx, KindAgent)
	if err != nil {
		return nil, err
	}
	records = append(records, agentRecords...)
	goRecords, err := s.scanKind(ctx, KindGo)
	if err != nil {
		return nil, err
	}
	records = append(records, goRecords...)
	seen := map[string]bool{}
	out := make([]Record, 0, len(records))
	for _, record := range records {
		if record.Name == "" || seen[record.Name] {
			continue
		}
		seen[record.Name] = true
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s FilesystemScanner) scanKind(ctx context.Context, kind Kind) ([]Record, error) {
	dir := filepath.Join(s.Root, dirNameForKind(kind))
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skill dir %q: %w", dir, err)
	}
	records := []Record{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(dir, entry.Name())
		record, ok, err := s.readRecord(root, entry.Name(), kind)
		if err != nil {
			return nil, err
		}
		if ok {
			records = append(records, record)
		}
	}
	return records, nil
}

func (s FilesystemScanner) readRecord(root, dirName string, kind Kind) (Record, bool, error) {
	if kind == KindGo {
		return s.readGoRecord(root, dirName)
	}
	return s.readAgentRecord(root, dirName)
}

func (s FilesystemScanner) readGoRecord(root, dirName string) (Record, bool, error) {
	data, err := os.ReadFile(filepath.Join(root, elyph.SkillFileName))
	if os.IsNotExist(err) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("read %s in %q: %w", elyph.SkillFileName, root, err)
	}
	doc, err := elyph.ParseSkill(string(data), dirName)
	if err != nil {
		return Record{}, false, fmt.Errorf("parse %s in %q: %w", elyph.SkillFileName, root, err)
	}
	binary, _, err := findGoBinary(root, dirName, doc.Name)
	if err != nil {
		return Record{}, false, err
	}
	detail := strings.TrimSpace(string(data))
	record := Record{Name: doc.Name, Description: firstElyphDescription(detail, doc.Name), Detail: detail, Format: elyph.Format, Risk: elyphRisk(detail), Kind: KindGo, Root: root, BinaryPath: binary}
	return record, true, nil
}

func (s FilesystemScanner) readAgentRecord(root, dirName string) (Record, bool, error) {
	if data, err := os.ReadFile(filepath.Join(root, elyph.SkillFileName)); err == nil {
		doc, err := elyph.ParseSkill(string(data), dirName)
		if err != nil {
			return Record{}, false, fmt.Errorf("parse %s in %q: %w", elyph.SkillFileName, root, err)
		}
		detail := strings.TrimSpace(string(data))
		return Record{Name: doc.Name, Description: firstElyphDescription(detail, doc.Name), Detail: detail, Format: elyph.Format, Risk: elyphRisk(detail), Kind: KindAgent, Root: root}, true, nil
	} else if !os.IsNotExist(err) {
		return Record{}, false, fmt.Errorf("read %s in %q: %w", elyph.SkillFileName, root, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "SKILL.md"))
	if os.IsNotExist(err) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("read SKILL.md in %q: %w", root, err)
	}
	def, err := ParseSkillMarkdown(data, dirName)
	if err != nil {
		return Record{}, false, fmt.Errorf("parse SKILL.md in %q: %w", root, err)
	}
	record := Record{Name: def.Name, Description: def.Description, Detail: def.Detail, Format: def.Format, Risk: def.Risk, Kind: KindAgent, Root: root}
	return record, true, nil
}

func firstElyphDescription(text, fallback string) string {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#skill ") {
			if _, desc, ok := strings.Cut(trimmed, " - "); ok {
				return strings.TrimSpace(desc)
			}
			break
		}
	}
	return fallback
}

func elyphRisk(text string) tool.RiskLevel {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "** risk ") {
			if risk, err := parseRisk(strings.TrimSpace(strings.TrimPrefix(trimmed, "** risk "))); err == nil {
				return risk
			}
		}
	}
	return tool.RiskHigh
}

func dirNameForKind(kind Kind) string {
	if kind == KindGo {
		return "go"
	}
	return "agent"
}

func findGoBinary(root, dirName, skillName string) (string, bool, error) {
	candidates := []string{}
	if runtime.GOOS == "windows" {
		candidates = append(candidates, filepath.Join(root, dirName+".exe"), filepath.Join(root, skillName+".exe"))
	} else {
		candidates = append(candidates, filepath.Join(root, dirName), filepath.Join(root, skillName))
	}
	for _, candidate := range candidates {
		if executableFile(candidate) {
			return candidate, true, nil
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", false, fmt.Errorf("read go skill dir %q: %w", root, err)
	}
	matches := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if runtime.GOOS == "windows" {
			if filepath.Ext(entry.Name()) == ".exe" {
				matches = append(matches, path)
			}
		} else if executableFile(path) {
			matches = append(matches, path)
		}
	}
	if len(matches) == 1 {
		return matches[0], true, nil
	}
	return "", false, nil
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return filepath.Ext(path) == ".exe"
	}
	return info.Mode()&0o111 != 0
}
