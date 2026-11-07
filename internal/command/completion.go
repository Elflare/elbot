// 提供命令入口共用的光标定位与静态候选补全。
package command

import "elbot/internal/command/share"

type CompletionToken = share.CompletionToken
type CompletionOption = share.CompletionOption

func CurrentCompletionToken(req CompletionRequest) CompletionToken {
	return share.CurrentCompletionToken(req)
}

func CompleteStaticOptions(options []CompletionOption, query string, start, end int, kind string) []Completion {
	return share.CompleteStaticOptions(options, query, start, end, kind)
}

func IsFirstArg(req CompletionRequest, token CompletionToken) bool {
	return share.IsFirstArg(req, token)
}
