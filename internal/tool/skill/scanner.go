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
		tools = append(tools, toolForRecord(record))
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
	replacements := make([]tool.Tool, 0, len(records))
	for _, record := range records {
		replacements = append(replacements, toolForRecord(record))
	}
	if err := registry.ReplaceSources([]tool.Source{tool.SourceSkillAgent, tool.SourceSkillGo}, replacements); err != nil {
		return err
	}
	if s.Catalog != nil {
		s.Catalog.Replace(records)
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
	seen := map[string]Record{}
	out := make([]Record, 0, len(records))
	duplicates := []string{}
	for _, record := range records {
		if record.Name == "" {
			continue
		}
		if previous, ok := seen[record.Name]; ok {
			duplicates = append(duplicates, fmt.Sprintf("%q (%s, %s)", record.Name, previous.Root, record.Root))
			continue
		}
		seen[record.Name] = record
		out = append(out, record)
	}
	if len(duplicates) > 0 {
		sort.Strings(duplicates)
		return nil, fmt.Errorf("duplicate skill names: %s", strings.Join(duplicates, "; "))
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
	header, err := elyph.ParseHeader(string(data))
	if err != nil {
		return Record{}, false, nil
	}
	binary, _, err := findGoBinary(root, dirName, header.Name)
	if err != nil {
		return Record{}, false, err
	}
	detail := strings.TrimSpace(string(data))
	record := Record{Name: header.Name, Description: header.Description, Detail: detail, Format: elyph.Format, Risk: elyphRisk(detail), Kind: KindGo, Root: root, BinaryPath: binary}
	return record, true, nil
}

func (s FilesystemScanner) readAgentRecord(root, dirName string) (Record, bool, error) {
	if data, err := os.ReadFile(filepath.Join(root, elyph.SkillFileName)); err == nil {
		header, err := elyph.ParseHeader(string(data))
		if err != nil {
			return Record{}, false, nil
		}
		detail := strings.TrimSpace(string(data))
		record := Record{Name: header.Name, Description: header.Description, Detail: detail, Format: elyph.Format, Risk: tool.RiskSafe, Kind: KindAgent, Root: root}
		return s.withAgentManifest(record), true, nil
	} else if !os.IsNotExist(err) {
		return Record{}, false, fmt.Errorf("read %s in %q: %w", elyph.SkillFileName, root, err)
	}
	detailPath := filepath.Join(root, "SKILL.md")
	data, err := os.ReadFile(detailPath)
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
	record := Record{Name: strings.Clone(def.Name), Description: strings.Clone(def.Description), DetailPath: detailPath, Format: def.Format, Risk: tool.RiskSafe, Kind: KindAgent, Root: root}
	return s.withAgentManifest(record), true, nil
}

func (s FilesystemScanner) withAgentManifest(record Record) Record {
	manifest, found, err := LoadAgentSkillManifest(record.Root)
	record.ManifestFound = found
	if err != nil {
		record.ManifestError = err.Error()
		return record
	}
	if found {
		record.Manifest = manifest
		record.Risk = manifest.Risk
		record.SuperadminOnly = manifest.SuperadminOnly
		record.Tags = manifest.Tags
	}
	return record
}

func toolForRecord(record Record) tool.Tool {
	if record.Kind == KindAgent && record.ManifestFound && record.ManifestError == "" && record.Manifest.Callable {
		return NewCommandTool(record)
	}
	return NewDescriptor(record)
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
