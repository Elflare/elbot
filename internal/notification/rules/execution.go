package rules

const TurnTimeout = "本轮处理已超时停止，可继续发送消息恢复或重试。"

func ExecutionFailure(err error) string { return "请求失败：" + err.Error() }
