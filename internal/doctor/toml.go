package doctor

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"elbot/internal/config"
	"elbot/internal/hook/rules"
	"elbot/internal/platform/cli"
	qqonebot "elbot/internal/platform/qq-onebot"
	"elbot/internal/platform/qqofficial"
	"elbot/internal/platform/telegram"
	"github.com/pelletier/go-toml/v2"
)

// The explicit Platform field replaces Config's open map for Doctor only.
// Named clients/providers remain maps, and extra_payload remains open-ended.
type mainConfig struct {
	config.Config
	Platform struct {
		CLI        cli.Config        `toml:"cli"`
		QQOneBot   qqonebot.Config   `toml:"qqonebot"`
		QQOfficial qqofficial.Config `toml:"qqofficial"`
		Telegram   telegram.Config   `toml:"telegram"`
	} `toml:"platform"`
}

func parseTOML(report *Report, path string, data []byte) map[string]any {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		kind := "TOML 格式错误"
		if strings.Contains(err.Error(), "already defined") || strings.Contains(err.Error(), "already exists") {
			kind = "重复定义"
		}
		report.add(path, decodeProblem(kind, err))
		return nil
	}
	if raw == nil {
		raw = make(map[string]any)
	}
	return raw
}

func inspectConfig(report *Report, path, name string, data []byte, template string) (map[string]any, error) {
	raw := parseTOML(report, path, data)
	if raw == nil {
		return nil, nil
	}
	var target any
	switch name {
	case "app.toml":
		target = &mainConfig{}
	case "providers.toml":
		target = &config.Config{}
	case "state.toml":
		target = &config.StateConfig{}
	case "elnis.toml":
		target = &config.ElnisConfig{}
	case "tool_tags.toml":
		target = &config.ToolTagsConfig{}
	case "plugins/hooks.toml":
		target = &rules.Config{}
	}
	decoder := toml.NewDecoder(bytes.NewReader(data))
	if name == "app.toml" {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		var unknown *toml.StrictMissingError
		if errors.As(err, &unknown) {
			var keys []string
			for _, field := range unknown.Errors {
				keys = append(keys, keyPath(field.Key()))
			}
			slices.Sort(keys)
			for _, key := range slices.Compact(keys) {
				report.add(path, "未知字段："+key)
			}
		} else {
			report.add(path, decodeProblem("字段类型错误", err))
			return raw, nil
		}
	}
	var expected map[string]any
	if err := toml.Unmarshal([]byte(template), &expected); err != nil {
		return raw, fmt.Errorf("doctor: parse bundled %s: %w", name, err)
	}
	missingFields(report, path, nil, raw, expected)
	return raw, nil
}

func decodeProblem(kind string, err error) string {
	var decodeErr *toml.DecodeError
	if errors.As(err, &decodeErr) {
		line, column := decodeErr.Position()
		return fmt.Sprintf("%s（第 %d 行，第 %d 列）：%s", kind, line, column, err)
	}
	return kind + "：" + err.Error()
}

func missingFields(report *Report, path string, prefix []string, actual, expected map[string]any) {
	keys := make([]string, 0, len(expected))
	for key := range expected {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		parts := append(append([]string(nil), prefix...), key)
		wantTable, table := expected[key].(map[string]any)
		value, exists := actual[key]
		if !exists {
			kind := "缺失字段："
			if table {
				kind = "缺失节点："
			}
			report.add(path, kind+keyPath(parts))
			continue
		}
		if gotTable, ok := value.(map[string]any); table && ok {
			missingFields(report, path, parts, gotTable, wantTable)
		}
	}
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
