// keyboard.go 将业务操作转换为 Telegram 命令按钮；按钮回调处理归 message.go。

package telegram

import "elbot/internal/delivery"

func commandKeyboard(actions []delivery.Action) *replyMarkup {
	var rows [][]inlineKeyboardButton
	for i, action := range actions {
		if i%2 == 0 {
			rows = append(rows, nil)
		}
		rows[len(rows)-1] = append(rows[len(rows)-1], commandButton(action.Label, action.Command))
	}
	return &replyMarkup{InlineKeyboard: rows}
}

func commandButton(label, command string) inlineKeyboardButton {
	return inlineKeyboardButton{Text: label, CallbackData: command}
}
