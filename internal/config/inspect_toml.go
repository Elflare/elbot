package config

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

func inspectDocument(doc *document, definition Definition, template string) ([]Issue, error) {
	if doc.err != nil {
		kind := doc.kind
		if kind == "" {
			kind = "无法读取文件"
		}
		if strings.Contains(doc.err.Error(), "already defined") || strings.Contains(doc.err.Error(), "already exists") {
			kind = "重复定义"
		}
		field := decodeField(doc.data, doc.err)
		return []Issue{{Path: doc.path, Field: field, Level: LevelError, Topic: topicFor(definition, field), Message: decodeProblem(kind, doc.err)}}, nil
	}
	var issues []Issue
	if definition.UnknownFields && definition.New != nil {
		decoder := toml.NewDecoder(bytes.NewReader(doc.data))
		decoder.DisallowUnknownFields()
		var unknown *toml.StrictMissingError
		if err := decoder.Decode(definition.New()); errors.As(err, &unknown) {
			slices.SortFunc(unknown.Errors, func(a, b toml.DecodeError) int { return strings.Compare(keyPath(a.Key()), keyPath(b.Key())) })
			for _, field := range unknown.Errors {
				issues = append(issues, Issue{Path: doc.path, Field: field.Key(), Level: LevelHint, Topic: topicFor(definition, field.Key()), Message: "未知字段：" + keyPath(field.Key()) + "；代码不会读取此字段。"})
			}
		}
	}
	var expected map[string]any
	if err := toml.Unmarshal([]byte(template), &expected); err != nil {
		return nil, fmt.Errorf("parse bundled %s: %w", definition.Asset, err)
	}
	expected = templateFields(expected, definition.Rules, nil)
	issues = append(issues, missingFields(doc.path, doc.raw, expected, definition, nil)...)
	// Rules can require a field not shown in the optional bundled template.
	for _, issue := range CheckRules(doc.raw, definition.Rules) {
		if ruleAt(definition.Rules, issue.Field).Effective {
			continue
		}
		if _, covered := lookup(expected, issue.Field); covered {
			if _, exists := lookup(doc.raw, issue.Field); !exists {
				continue
			}
		}
		issue.Path = doc.path
		if issue.Topic == "" {
			issue.Topic = topicFor(definition, issue.Field)
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

func templateFields(table map[string]any, rules []Rule, prefix []string) map[string]any {
	filtered := make(map[string]any)
	for key, value := range table {
		path := append(slices.Clone(prefix), key)
		rule := ruleAt(rules, path)
		if rule.Example || rule.Effective {
			continue
		}
		if child, ok := value.(map[string]any); ok {
			next := templateFields(child, rules, path)
			if len(next) == 0 && len(child) > 0 {
				continue // No empty-parent hint when all children are examples.
			}
			value = next
		}
		filtered[key] = value
	}
	return filtered
}

func missingFields(path string, actual, expected map[string]any, definition Definition, prefix []string) []Issue {
	var issues []Issue
	keys := make([]string, 0, len(expected))
	for key := range expected {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		field := append(slices.Clone(prefix), key)
		wantTable, table := expected[key].(map[string]any)
		value, exists := actual[key]
		if !exists {
			kind := "缺失字段："
			if table {
				kind = "缺失节点："
			}
			rule := ruleAt(definition.Rules, field)
			level, effect := LevelHint, rule.Hint
			if rule.Presence == Required {
				level = LevelError
				if effect == "" {
					effect = "此配置必需。"
				}
			} else if effect == "" {
				effect = "可省略，按代码默认行为处理。"
			}
			issues = append(issues, Issue{Path: path, Field: field, Missing: true, Level: level, Topic: topicFor(definition, field), Message: kind + keyPath(field) + "；" + effect})
			continue
		}
		if gotTable, ok := value.(map[string]any); table && ok {
			issues = append(issues, missingFields(path, gotTable, wantTable, definition, field)...)
		}
	}
	return issues
}

func topicFor(definition Definition, field []string) string {
	if topic := ruleAt(definition.Rules, field).Topic; topic != "" {
		return topic
	}
	return definition.Topic
}

func decodeProblem(kind string, err error) string {
	var decodeErr *toml.DecodeError
	if errors.As(err, &decodeErr) {
		line, column := decodeErr.Position()
		return fmt.Sprintf("%s（第 %d 行，第 %d 列）：%s", kind, line, column, decodeErr)
	}
	return kind + "：" + err.Error()
}

// Decoder type errors may omit Key. Resolve the original source token instead
// of guessing from text, so quoted keys and strings containing TOML are safe.
func decodeField(data []byte, err error) []string {
	var decodeErr *toml.DecodeError
	if !errors.As(err, &decodeErr) {
		return nil
	}
	if keys := decodeErr.Key(); len(keys) > 0 {
		return keys
	}
	line, column := decodeErr.Position()
	offset := column - 1
	for start, row := 0, 1; row < line; row++ {
		next := bytes.IndexByte(data[start:], '\n')
		if next < 0 {
			return nil
		}
		start += next + 1
		offset += next + 1
	}
	var parser unstable.Parser
	parser.Reset(data)
	var table []string
	for parser.NextExpression() {
		node := parser.Expression()
		if node.Kind == unstable.Table || node.Kind == unstable.ArrayTable {
			table = tomlNodeKeys(node)
		}
		if field := nodeField(node, table, offset); field != nil {
			return field
		}
	}
	return nil
}

func tomlNodeKeys(node *unstable.Node) []string {
	var keys []string
	for it := node.Key(); it.Next(); {
		keys = append(keys, string(it.Node().Data))
	}
	return keys
}

func nodeField(node *unstable.Node, prefix []string, offset int) []string {
	if node.Kind == unstable.KeyValue {
		prefix = append(slices.Clone(prefix), tomlNodeKeys(node)...)
	}
	for it := node.Children(); it.Next(); {
		if field := nodeField(it.Node(), prefix, offset); field != nil {
			return field
		}
	}
	if offset >= int(node.Raw.Offset) && offset < int(node.Raw.Offset+node.Raw.Length) {
		return slices.Clone(prefix)
	}
	return nil
}

func keyPath(parts []string) string {
	quoted := make([]string, len(parts))
	for i, part := range parts {
		if part != "" && strings.IndexFunc(part, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
		}) < 0 {
			quoted[i] = part
		} else {
			quoted[i] = strconv.Quote(part)
		}
	}
	return strings.Join(quoted, ".")
}
