package telegram

import "strings"

func shouldAttachRiskKeyboard(text string) bool {
	return strings.Contains(text, "高风险工具调用等待确认") && strings.Contains(text, "/confirm")
}

func riskKeyboard() *replyMarkup {
	return &replyMarkup{InlineKeyboard: [][]inlineKeyboardButton{
		{
			commandButton("详情", "/detail"),
			commandButton("确认", "/confirm"),
		},
		{
			commandButton("确认此工具", "/confirmtool"),
			commandButton("全部确认", "/confirmall"),
		},
		{
			commandButton("拒绝", "/reject"),
			commandButton("停止", "/stop"),
		},
	}}
}

func commandButton(label, command string) inlineKeyboardButton {
	return inlineKeyboardButton{Text: label, CallbackData: command}
}
