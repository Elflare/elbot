package share

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/workspace"
)

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

func BuildReportOutputs(report string, segments []delivery.Output, scope workspace.PathScope) ([]delivery.Output, error) {
	outputs := []delivery.Output{}
	if text := strings.TrimSpace(report); text != "" {
		outputs = append(outputs, delivery.Text(text))
	}
	for i, segment := range segments {
		out, err := BuildReportSegmentOutput(segment, scope)
		if err != nil {
			return nil, fmt.Errorf("report segment %d: %w", i, err)
		}
		outputs = append(outputs, out)
	}
	return outputs, nil
}

func BuildReportSegmentOutput(segment delivery.Output, scope workspace.PathScope) (delivery.Output, error) {
	if segment.Source.Path == "" {
		return segment, nil
	}
	if scope.Dir == "" {
		return delivery.Output{}, fmt.Errorf("report workspace is required")
	}
	path := filepath.Join(scope.Dir, segment.Source.Path)
	info, err := os.Stat(path)
	if err != nil {
		return delivery.Output{}, fmt.Errorf("stat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return delivery.Output{}, fmt.Errorf("report media must be a regular file")
	}
	segment.Source.Path = path
	return segment, nil
}
