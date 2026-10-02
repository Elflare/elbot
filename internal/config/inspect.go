package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Inspection struct {
	Config *Config
	Issues []Issue
}

type Inspector struct {
	definitions []Definition
}

// NewInspector accepts explicitly assembled module schemas. It has no global
// registration and never resolves credentials or initializes runtime resources.
func NewInspector(definitions ...Definition) *Inspector {
	return &Inspector{definitions: slices.Clone(definitions)}
}

func (i *Inspector) Inspect(ctx context.Context, path string) (Inspection, error) {
	if strings.TrimSpace(path) == "" {
		return Inspection{}, fmt.Errorf("config: inspection path is empty")
	}
	if err := ctx.Err(); err != nil {
		return Inspection{}, err
	}
	state := newConfiguration(path, i.definitions)
	if err := state.readCore(ctx, true); err != nil {
		return Inspection{}, err
	}
	result := Inspection{Config: state.cfg}
	names := state.assetOrder()
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return Inspection{}, err
		}
		asset := state.assets[name]
		if asset.Example {
			continue
		}
		path, err := state.assetPath(name)
		if err != nil {
			field, topic := asset.Reference, asset.Topic
			if main := state.documents[state.root]; main == nil || main.raw == nil {
				field, topic = nil, ""
			}
			result.add(Issue{Path: state.paths[state.root], Field: field, Level: LevelHint, Topic: topic, Message: err.Error()})
			continue
		}
		definition, schema := state.definition(name)
		if definition.Topic == "" {
			definition.Topic = asset.Topic
		}
		if !schema && !asset.CompareContent && !asset.TOML {
			info, err := os.Stat(path)
			if err != nil {
				result.add(assetReadIssue(asset, path, err))
			} else if !info.Mode().IsRegular() {
				result.add(Issue{Path: path, Level: LevelError, Topic: asset.Topic, Message: "路径不是普通文件。"})
			}
			continue
		}
		doc := state.documents[name]
		if doc == nil {
			if schema || asset.TOML {
				doc = readDocument(path, definition.New)
			} else {
				doc = &document{path: path}
				doc.data, doc.err = os.ReadFile(path)
				doc.missing = os.IsNotExist(doc.err)
			}
			state.documents[name] = doc
		}
		if doc.missing || doc.err != nil && doc.kind == "" {
			result.add(assetReadIssue(asset, path, doc.err))
			if definition.Apply != nil && (!doc.missing || asset.Presence == Required) {
				result.add(Issue{Path: path, Level: LevelHint, Topic: definition.Topic, Message: "配置无法读取，依赖此文件的检查未完成。"})
			}
			continue
		}
		if schema || asset.TOML {
			template := ""
			if schema {
				template = asset.Content
			}
			issues, err := inspectDocument(doc, definition, template)
			if err != nil {
				return Inspection{}, err
			}
			result.add(issues...)
		}
		if doc.err != nil {
			if definition.Apply != nil {
				result.add(Issue{Path: path, Level: LevelHint, Topic: definition.Topic, Message: "配置无法解析，依赖此文件的检查未完成。"})
			}
			continue
		}
		if schema {
			result.add(state.inspectModules(doc, definition)...)
		}
		if asset.CompareContent && withoutNewlines(doc.data) != withoutNewlines([]byte(asset.Content)) {
			result.add(Issue{Path: path, Level: LevelHint, Topic: asset.Topic, Message: "内容与当前内置版本不同，需要查看差异。"})
		}
	}
	validation := ValidationContext{Config: state.cfg, state: state}
	for _, definition := range state.definitions {
		if definition.Validate != nil {
			for _, issue := range definition.Validate(validation) {
				if issue.Path == "" {
					issue.Path = state.paths[definition.Asset]
				}
				if issue.Topic == "" {
					issue.Topic = topicFor(definition, issue.Field)
				}
				result.add(issue)
			}
		}
	}
	order := map[string]int{}
	for n, name := range names {
		if path := state.paths[name]; path != "" {
			if _, exists := order[path]; !exists {
				order[path] = n
			}
		}
	}
	slices.SortStableFunc(result.Issues, func(a, b Issue) int { return order[a.Path] - order[b.Path] })
	return result, nil
}

func (s *configuration) assetOrder() []string {
	var names []string
	for _, definition := range s.definitions {
		if len(definition.Node) == 0 && !slices.Contains(names, definition.Asset) {
			names = append(names, definition.Asset)
		}
	}
	for _, compare := range []bool{false, true} {
		for _, asset := range DefaultAssets() {
			name := filepath.ToSlash(asset.Path)
			if asset.CompareContent == compare && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	return names
}

func (s *configuration) inspectModules(doc *document, root Definition) []Issue {
	var issues []Issue
	for _, definition := range s.definitions {
		if definition.Asset != root.Asset || len(definition.Node) == 0 || definition.New == nil {
			continue
		}
		raw, exists := lookup(doc.raw, definition.Node)
		if !exists {
			continue
		}
		data, err := toml.Marshal(raw)
		if err != nil {
			issues = append(issues, Issue{Path: doc.path, Field: definition.Node, Level: LevelError, Topic: definition.Topic, Message: "字段类型错误：" + keyPath(definition.Node)})
			continue
		}
		child := &document{path: doc.path, data: data}
		decodeDocument(child, definition.New)
		found, err := inspectDocument(child, definition, "")
		if err != nil {
			continue // There is no template to fail parsing here.
		}
		for _, issue := range found {
			relative := keyPath(issue.Field)
			issue.Field = append(slices.Clone(definition.Node), issue.Field...)
			if issue.Level == LevelError && child.err != nil {
				// Child data was encoded from the parsed subtree; its line
				// numbers are not positions in the original file.
				detail := child.err.Error()
				var decodeErr *toml.DecodeError
				if errors.As(child.err, &decodeErr) {
					detail = decodeErr.Error()
				}
				issue.Message = "字段类型错误：" + keyPath(issue.Field) + "；" + detail
			} else {
				issue.Message = strings.Replace(issue.Message, relative, keyPath(issue.Field), 1)
			}
			if issue.Topic == "" {
				issue.Topic = topicFor(root, issue.Field)
			}
			issues = append(issues, issue)
		}
	}
	for _, rule := range flattenRules(root.Rules, nil) {
		if !rule.ClosedChildren {
			continue
		}
		var allowed []string
		for _, definition := range s.definitions {
			if definition.Asset == root.Asset && len(definition.Node) == len(rule.Path)+1 && hasPrefix(definition.Node, rule.Path) {
				allowed = append(allowed, definition.Node[len(rule.Path)])
			}
		}
		if len(allowed) == 0 {
			continue // No module registry was supplied for this subtree.
		}
		value, _ := lookup(doc.raw, rule.Path)
		children, _ := value.(map[string]any)
		var unknown []string
		for key := range children {
			if !slices.Contains(allowed, key) {
				unknown = append(unknown, key)
			}
		}
		slices.Sort(unknown)
		for _, key := range unknown {
			field := append(slices.Clone(rule.Path), key)
			issues = append(issues, Issue{Path: doc.path, Field: field, Level: LevelHint, Topic: topicFor(root, field), Message: "未知字段：" + keyPath(field) + "；代码不会读取此字段。"})
		}
	}
	return issues
}

func assetReadIssue(asset DefaultAsset, path string, err error) Issue {
	issue := Issue{Path: path, Level: LevelError, Topic: asset.Topic, Message: "无法读取文件：" + err.Error()}
	if errors.Is(err, os.ErrNotExist) {
		issue.Missing = true
		issue.Message = "文件缺失。"
		if asset.Presence == Required {
			issue.Message += "当前配置必须读取此文件。"
		} else {
			issue.Level = LevelHint
			issue.Message += asset.MissingHint
		}
	}
	return issue
}

func (r *Inspection) add(issues ...Issue) {
	for _, issue := range issues {
		skip := false
		for _, previous := range r.Issues {
			same := previous.Path == issue.Path && previous.Level == issue.Level && previous.Topic == issue.Topic && previous.Message == issue.Message && slices.Equal(previous.Field, issue.Field)
			parent := previous.Path == issue.Path && previous.Missing && len(previous.Field) > 0 && len(issue.Field) > len(previous.Field) && hasPrefix(issue.Field, previous.Field)
			if same || parent {
				skip = true
				break
			}
		}
		if !skip {
			if issue.Missing && len(issue.Field) > 0 {
				r.Issues = slices.DeleteFunc(r.Issues, func(previous Issue) bool {
					return previous.Path == issue.Path && previous.Missing && len(previous.Field) > len(issue.Field) && hasPrefix(previous.Field, issue.Field)
				})
			}
			r.Issues = append(r.Issues, issue)
		}
	}
}

func withoutNewlines(data []byte) string {
	return strings.ReplaceAll(strings.ReplaceAll(string(data), "\r", ""), "\n", "")
}
