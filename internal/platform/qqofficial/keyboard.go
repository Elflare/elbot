// keyboard.go 将业务提供的操作转换为 QQ 命令按钮与键盘载荷。
// 这里只生成平台消息组件，不执行按钮对应的业务命令。

package qqofficial

import "elbot/internal/delivery"

func commandKeyboard(appID string, actions []delivery.Action) *messageKeyboard {
	var rows []keyboardRow
	for i, action := range actions {
		if i%2 == 0 {
			rows = append(rows, keyboardRow{})
		}
		rows[len(rows)-1].Buttons = append(rows[len(rows)-1].Buttons, commandButton(action))
	}
	return &messageKeyboard{Content: &inlineKeyboardContent{
		BotAppID: appID,
		Rows:     rows,
	}}
}

func commandButton(action delivery.Action) keyboardButton {
	return keyboardButton{
		ID: action.ID,
		RenderData: keyboardRenderData{
			Label:        action.Label,
			VisitedLabel: action.Label,
		},
		Action: keyboardButtonAction{
			Type: 2,
			Permission: keyboardPermission{
				Type: 2,
			},
			ClickLimit:    10,
			UnsupportTips: action.Command,
			Data:          action.Command,
			Enter:         true,
		},
	}
}
