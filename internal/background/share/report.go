package share

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"elbot/internal/delivery"
	"elbot/internal/media"
	"elbot/internal/workspace"
)

// ReportMedia 只接收已在任务目录边界内打开的附件流。
type ReportMedia interface {
	ImportReader(context.Context, io.Reader, int64, media.Input, media.ImportLimits) (media.MediaInfo, error)
}

type ReportMessageMap struct {
	Platform          string // 实际发送平台。
	PlatformScopeID   string // 实际发送会话。
	PlatformMessageID string // 平台返回的消息标识。
	SessionID         string // 报告所属会话。
	MessageID         string // 报告对应的持久化消息。
}

// MapReportReceipt 只映射发送方明确返回的消息归属，不推测平台或会话。
func MapReportReceipt(sessionID, messageID string, receipt delivery.Receipt) []ReportMessageMap {
	if sessionID == "" || messageID == "" {
		return nil
	}
	mappings := make([]ReportMessageMap, 0, len(receipt.SentMessages))
	for _, sent := range receipt.SentMessages {
		mappings = append(mappings, ReportMessageMap{Platform: sent.Platform, PlatformScopeID: sent.ScopeID, PlatformMessageID: sent.PlatformMessageID, SessionID: sessionID, MessageID: messageID})
	}
	return mappings
}

func PathInstruction() string {
	return "后台任务的本地附件路径必须基于任务工作目录，使用相对路径；不得使用绝对路径、~、冒号或 .. 路径段。"
}

// BuildReportSegmentOutput 在任务目录内打开本地附件，只向发送方交付媒体 ID。
func BuildReportSegmentOutput(ctx context.Context, segment delivery.Output, scope workspace.PathScope, importer ReportMedia, limits media.ImportLimits) (out delivery.Output, err error) {
	if segment.Source.Path == "" {
		return segment, nil
	}
	root, err := os.OpenRoot(scope.Dir)
	if err != nil {
		return delivery.Output{}, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	file, err := root.Open(segment.Source.Path)
	if err != nil {
		return delivery.Output{}, fmt.Errorf("open report media: %w", err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	stat, err := file.Stat()
	if err != nil {
		return delivery.Output{}, err
	}
	if !stat.Mode().IsRegular() {
		return delivery.Output{}, fmt.Errorf("report media must be a regular file")
	}
	input := media.Input{Name: segment.Name, MIMEType: segment.Source.MIMEType}
	if input.Name == "" {
		input.Name = filepath.Base(segment.Source.Path)
	}
	info, err := importer.ImportReader(ctx, file, stat.Size(), input, limits)
	if err != nil {
		return delivery.Output{}, err
	}
	segment.Source = delivery.Source{MediaID: info.ID, MIMEType: info.MIMEType}
	segment.Name = info.Name
	return segment, nil
}
