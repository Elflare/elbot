package transfer

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"elbot/internal/llm"
	"elbot/internal/media/share"
)

// requestUploadConcurrency 限制单次模型请求的上传和签名并发。
const requestUploadConcurrency = 8

// AcquireForLLM 克隆请求、按 ID 复用持有，补齐信息并按总大小选择交付方式。
// 内联使用 held.Open，远端使用 held.SignedURL，不接收存储记录。
// 使用记录归 Media；准备失败在工作退出后收尾，失败资源由维护继续处理。
func (m *Manager) AcquireForLLM(ctx context.Context, messages []llm.LLMMessage) (_ []llm.LLMMessage, usageID string, err error) {
	out := llm.CloneMessages(messages)
	defer func() {
		if err != nil {
			err = errors.Join(err, m.abortUsage(ctx, usageID))
			usageID = ""
		}
	}()
	mediaByID := map[string]share.HeldMedia{}
	var items []share.HeldMedia
	var total int64
	for i := range out {
		for j, segment := range out[i].Segments {
			if err := ctx.Err(); err != nil {
				return nil, usageID, err
			}
			if (segment.Type != llm.SegmentImage && segment.Type != llm.SegmentFile) || segment.MediaID == "" {
				continue
			}
			held, ok := mediaByID[segment.MediaID]
			if !ok {
				held, err = m.objects.Hold(ctx, segment.MediaID)
				if err != nil && !share.IsUnavailable(err) {
					return nil, usageID, err
				}
				if err == nil {
					usageID = m.addHeld(usageID, held)
					items = append(items, held)
				}
				mediaByID[segment.MediaID] = held
			}
			if held == nil {
				segment.Name = share.SanitizeName(segment.Name)
				out[i].Segments[j] = unavailable(segment)
				continue
			}
			metadata := held.Info()
			out[i].Segments[j].Name, out[i].Segments[j].MIMEType = metadata.Name, metadata.MIMEType
			total += metadata.Size
		}
	}
	remote := m.options.Backend == "s3" || (m.options.Backend == "hybrid" && total > m.options.MaxDirectBase64Bytes)
	if !remote && total > m.options.MaxDirectBase64Bytes {
		return nil, usageID, fmt.Errorf("media request is %d bytes, exceeds file_delivery.max_direct_base64_bytes=%d", total, m.options.MaxDirectBase64Bytes)
	}
	urls := map[string]string{}
	if remote {
		urls, err = m.prepareRemoteURLs(ctx, items)
		if err != nil {
			return nil, usageID, err
		}
	}
	for i := range out {
		for j, segment := range out[i].Segments {
			held := mediaByID[segment.MediaID]
			if held == nil || (segment.Type != llm.SegmentImage && segment.Type != llm.SegmentFile) {
				continue
			}
			value, ok := urls[segment.MediaID]
			if !ok {
				data, err := m.readMedia(ctx, held)
				if err != nil && !share.IsUnavailable(err) {
					return nil, usageID, err
				}
				if err == nil {
					value = "data:" + segment.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(data)
				}
				urls[segment.MediaID] = value
			}
			if value == "" {
				out[i].Segments[j] = unavailable(segment)
			} else {
				out[i].Segments[j].URL = value
			}
		}
	}
	return out, usageID, ctx.Err()
}

// prepareRemoteURLs 并发处理去重后的持有句柄，全部 worker 退出后才返回。
// 地址结果按媒体 ID 组织；副本上传和签名缓存由句柄所属对象组件完成。
func (m *Manager) prepareRemoteURLs(ctx context.Context, items []share.HeldMedia) (map[string]string, error) {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]string, len(items))
	failures := make([]error, len(items))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(requestUploadConcurrency, len(items)) {
		workers.Go(func() {
			for i := range jobs {
				if workCtx.Err() != nil {
					return
				}
				value, err := items[i].SignedURL(workCtx)
				if err != nil {
					if share.IsUnavailable(err) {
						continue
					}
					failures[i] = err
					cancel()
					return
				}
				results[i] = value
			}
		})
	}
dispatch:
	for i := range items {
		if workCtx.Err() != nil {
			break
		}
		select {
		case <-workCtx.Done():
			break dispatch
		case jobs <- i:
		}
	}
	close(jobs)
	workers.Wait()
	if err := errors.Join(append(failures, ctx.Err())...); err != nil {
		return nil, err
	}
	urls := make(map[string]string, len(items))
	for i, item := range items {
		urls[item.Info().ID] = results[i]
	}
	return urls, nil
}

// readMedia 从已持有对象读取内容并关闭读取器，不结束对象持有。
func (m *Manager) readMedia(ctx context.Context, held share.HeldMedia) ([]byte, error) {
	reader, err := held.Open(ctx)
	if err != nil {
		return nil, errors.Join(err, ctx.Err())
	}
	data, err := io.ReadAll(reader)
	return data, errors.Join(err, reader.Close(), ctx.Err())
}

// unavailable 将不可用媒体替换为包含展示名称和媒体标识的文本说明。
func unavailable(segment llm.MessageSegment) llm.MessageSegment {
	return llm.MessageSegment{Type: llm.SegmentText, Text: "[媒体不可用；" + strings.TrimSpace(segment.Name) + "；媒体 ID：" + segment.MediaID + "]"}
}
