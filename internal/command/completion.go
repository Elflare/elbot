// 提供命令入口共用的光标定位与静态候选补全。
package command

import "strings"

type CompletionToken struct {
	Start int    // 当前词起始字节位置。
	End   int    // 光标所在的结束字节位置。
	Text  string // 当前词到光标前的内容。
}

func CurrentCompletionToken(req CompletionRequest) CompletionToken {
	cursor := req.Cursor
	if cursor <= 0 || cursor > len(req.Raw) {
		cursor = len(req.Raw)
	}
	start := cursor
	for start > 0 && req.Raw[start-1] != ' ' && req.Raw[start-1] != '\t' {
		start--
	}
	return CompletionToken{Start: start, End: cursor, Text: req.Raw[start:cursor]}
}

func CompleteStaticOptions(options []CompletionOption, query string, start, end int, kind string) []Completion {
	out := []Completion{}
	for _, option := range options {
		if query != "" && !strings.HasPrefix(option.Text, query) {
			continue
		}
		label := option.Label
		if strings.TrimSpace(label) == "" {
			label = option.Text
		}
		out = append(out, Completion{Text: option.Text, Label: label, Description: option.Description, Kind: kind, ReplaceStart: start, ReplaceEnd: end})
	}
	return out
}

type CompletionOption struct {
	Text        string // 补全后写入的文本。
	Label       string // 显示名称，空值使用 Text。
	Description string // 候选说明。
}

func IsFirstArg(req CompletionRequest, token CompletionToken) bool {
	before := strings.TrimSpace(req.Raw[len(req.Prefix)+len(req.Name) : token.Start])
	return before == ""
}
