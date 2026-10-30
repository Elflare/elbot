package command

import "strings"

type Parsed struct {
	OK     bool   // 是否匹配已配置的命令前缀，不表示命令已注册。
	Prefix string // 实际匹配的最长前缀。
	Name   string // 去除前缀后的命令名，统一小写。
	Args   string // 命令参数，保留内部空白。
}

// Parse 使用 config 已规范化的前缀做最长匹配，不修改配置顺序或补默认值。
func Parse(text string, prefixes []string) Parsed {
	text = strings.TrimSpace(text)
	var prefix string
	for _, candidate := range prefixes {
		if len(candidate) > len(prefix) && strings.HasPrefix(text, candidate) {
			prefix = candidate
		}
	}
	if prefix == "" {
		return Parsed{}
	}
	name, args, _ := strings.Cut(strings.TrimSpace(text[len(prefix):]), " ")
	return Parsed{OK: true, Prefix: prefix, Name: normalizeName(name), Args: strings.TrimSpace(args)}
}
