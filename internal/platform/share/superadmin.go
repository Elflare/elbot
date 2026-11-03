// superadmin.go 封装单个平台的超级管理员身份判断和私聊群发。
package share

import (
	"context"
	"fmt"
	"slices"

	"elbot/internal/delivery"
)

// Superadmins 由根部按平台构造；名单为已整理的原生用户 ID，构造后只读。
type Superadmins struct {
	platformName string   // 根部登记的平台名，用于发送目标和错误归属。
	ids          []string // 当前平台的超级管理员 ID，保留配置顺序。
}

func NewSuperadmins(platformName string, ids []string) *Superadmins {
	return &Superadmins{platformName: platformName, ids: slices.Clone(ids)}
}

func (s *Superadmins) IsSuperadmin(senderID string) bool {
	return slices.Contains(s.ids, senderID)
}

// SendTo 逐个发送私聊，遇错停止并保留已取得的回执；发送回调负责协议与连接。
func (s *Superadmins) SendTo(ctx context.Context, outputs []delivery.Output, send func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error)) (delivery.Receipt, error) {
	if len(s.ids) == 0 {
		err := fmt.Errorf("%s superadmins are not configured", s.platformName)
		return delivery.Receipt{}, err
	}
	var receipt delivery.Receipt
	for _, id := range s.ids {
		target := delivery.Target{Platform: s.platformName, ChatType: "private", ChatID: id}
		sent, err := send(ctx, target, outputs)
		receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, sent.PlatformMessageIDs...)
		receipt.SentMessages = append(receipt.SentMessages, sent.SentMessages...)
		if err != nil {
			return receipt, err
		}
	}
	return receipt, nil
}
