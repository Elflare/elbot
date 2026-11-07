package contextmgr

import (
	"context"
	"errors"
	"fmt"

	"elbot/internal/storage"
)

type LoadedContext struct {
	Summary  *storage.ContextSummary
	Messages []storage.Message
}

type Loader struct {
	Store storage.Store
}

const maxForkDepth = 32

func (l Loader) Load(ctx context.Context, sessionID string) (*LoadedContext, error) {
	return l.load(ctx, sessionID, "", 0)
}

func (l Loader) LoadRawMessages(ctx context.Context, sessionID string) ([]storage.Message, error) {
	return l.loadRawMessages(ctx, sessionID, "", 0)
}

func (l Loader) load(ctx context.Context, sessionID, upToMessageID string, depth int) (*LoadedContext, error) {
	if depth > maxForkDepth {
		return nil, fmt.Errorf("fork depth exceeds %d", maxForkDepth)
	}
	session, err := l.Store.Sessions().Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	loaded, err := l.loadOwnSummary(ctx, session.ID, upToMessageID)
	if err != nil {
		return nil, err
	}
	if loaded != nil {
		return loaded, nil
	}

	out := &LoadedContext{}
	if session.ParentSessionID != "" && session.ForkFromMessageID != "" {
		parent, err := l.load(ctx, session.ParentSessionID, session.ForkFromMessageID, depth+1)
		if err != nil {
			return nil, err
		}
		out.Summary = parent.Summary
		out.Messages = append(out.Messages, parent.Messages...)
	}

	messages, err := l.listOwnMessages(ctx, session.ID, upToMessageID)
	if err != nil {
		return nil, err
	}
	out.Messages = append(out.Messages, messages...)
	return out, nil
}

func (l Loader) loadOwnSummary(ctx context.Context, sessionID, upToMessageID string) (*LoadedContext, error) {
	var (
		summary *storage.ContextSummary
		err     error
	)
	if upToMessageID == "" {
		summary, err = l.Store.ContextSummaries().LatestBySession(ctx, sessionID)
	} else {
		summary, err = l.Store.ContextSummaries().LatestBySessionUpTo(ctx, sessionID, upToMessageID)
	}
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	messages, err := l.listOwnMessagesAfter(ctx, sessionID, summary.ToMessageID, upToMessageID)
	if err != nil {
		return nil, err
	}
	return &LoadedContext{Summary: summary, Messages: messages}, nil
}

func (l Loader) listOwnMessages(ctx context.Context, sessionID, upToMessageID string) ([]storage.Message, error) {
	if upToMessageID == "" {
		return l.Store.Messages().ListBySession(ctx, sessionID)
	}
	return l.Store.Messages().ListBySessionUpTo(ctx, sessionID, upToMessageID)
}

func (l Loader) listOwnMessagesAfter(ctx context.Context, sessionID, afterMessageID, upToMessageID string) ([]storage.Message, error) {
	var (
		messages []storage.Message
		err      error
	)
	if upToMessageID == "" {
		messages, err = l.Store.Messages().ListBySessionAfter(ctx, sessionID, afterMessageID)
	} else {
		messages, err = l.Store.Messages().ListBySessionAfterUpTo(ctx, sessionID, afterMessageID, upToMessageID)
	}
	if errors.Is(err, storage.ErrNotFound) {
		// A fork summary may cover an ancestor range, leaving only this branch's messages.
		return l.listOwnMessages(ctx, sessionID, upToMessageID)
	}
	return messages, err
}

func (l Loader) loadRawMessages(ctx context.Context, sessionID, upToMessageID string, depth int) ([]storage.Message, error) {
	if depth > maxForkDepth {
		return nil, fmt.Errorf("fork depth exceeds %d", maxForkDepth)
	}
	session, err := l.Store.Sessions().Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	var out []storage.Message
	if session.ParentSessionID != "" && session.ForkFromMessageID != "" {
		parent, err := l.loadRawMessages(ctx, session.ParentSessionID, session.ForkFromMessageID, depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, parent...)
	}
	own, err := l.listOwnMessages(ctx, session.ID, upToMessageID)
	if err != nil {
		return nil, err
	}
	return append(out, own...), nil
}

// MessagePage 是有界原始对话页，不经过模型上下文摘要。
type MessagePage struct {
	Messages   []storage.Message // 当前页，按对话顺序排列。
	TotalPages int               // 完整分支历史的页数。
}

// LoadMessagePage 从最新页向过去读取；只取得祖先范围计数和本页正文。
func (l Loader) LoadMessagePage(ctx context.Context, sessionID string, page, pageSize int) (MessagePage, error) {
	type historyRange struct {
		id, until string
		count     int
	}
	ranges := []historyRange{}
	until := ""
	total := 0
	inheritedCutoff := false
	for depth := 0; sessionID != ""; depth++ {
		if depth > maxForkDepth {
			return MessagePage{}, fmt.Errorf("fork depth exceeds %d", maxForkDepth)
		}
		row, err := l.Store.Sessions().Get(ctx, sessionID)
		if err != nil {
			return MessagePage{}, err
		}
		count, err := l.Store.Messages().CountConversationMessages(ctx, sessionID, until)
		if until != "" && errors.Is(err, storage.ErrNotFound) {
			// 截止消息可能继承自更早祖先；跳过本分支自身消息，继续寻找该截止位置。
			inheritedCutoff = true
			sessionID = row.ParentSessionID
			continue
		}
		if err != nil {
			return MessagePage{}, err
		}
		inheritedCutoff = false
		ranges = append(ranges, historyRange{id: sessionID, until: until, count: count})
		total += count
		sessionID, until = row.ParentSessionID, row.ForkFromMessageID
		if until == "" {
			break
		}
	}
	if inheritedCutoff {
		return MessagePage{}, fmt.Errorf("fork checkpoint %s not found in ancestry", until)
	}
	out := MessagePage{Messages: []storage.Message{}, TotalPages: (total + pageSize - 1) / pageSize}
	// 比较总页数后再乘，避免极大非法页码导致 offset 溢出。
	if page > max(1, out.TotalPages) {
		return out, nil
	}
	offset := (page - 1) * pageSize
	for _, span := range ranges {
		if offset >= span.count {
			offset -= span.count
			continue
		}
		rows, err := l.Store.Messages().ListConversationMessages(ctx, span.id, span.until, pageSize-len(out.Messages), offset)
		if err != nil {
			return MessagePage{}, err
		}
		out.Messages = append(out.Messages, rows...)
		offset = 0
		if len(out.Messages) == pageSize {
			break
		}
	}
	for i, j := 0, len(out.Messages)-1; i < j; i, j = i+1, j-1 {
		out.Messages[i], out.Messages[j] = out.Messages[j], out.Messages[i]
	}
	return out, nil
}
