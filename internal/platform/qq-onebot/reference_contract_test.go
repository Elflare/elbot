// reference_contract_test.go 用引用契约调用 OneBot 自身的 Fetch 和 Enrich。
// 夹具不模拟引用组件的存储查询、权限或续接／分叉决策。
package qqonebot

import (
	"context"
	"elbot/internal/platform/share"
)

func resolveFetchedReferenceForTest(ctx context.Context, opts share.ReferenceOptions) (share.ReferenceResult, error) {
	ref, ok, err := opts.Fetch(ctx, opts.ReplyID)
	if err != nil || !ok {
		return share.ReferenceResult{Text: opts.Text}, err
	}
	reply := share.ReplyContext{
		MessageID: opts.ReplyID, SenderID: ref.SenderID, SenderName: ref.SenderName,
		Text: ref.Text, Segments: ref.Segments,
	}
	display, err := opts.Enrich(ctx, opts.ReplyID, ref)
	if err != nil {
		return share.ReferenceResult{}, err
	}
	text := opts.Text
	if len(display) == 0 {
		text = share.FormatReferenceText(opts.Platform, reply, opts.Text)
	}
	return share.ReferenceResult{
		Text: text, Reply: reply, ReferenceSegments: ref.Segments, DisplaySegments: display,
	}, nil
}
