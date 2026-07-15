package agent

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (a *Agent) CompactCurrent(ctx context.Context, triggerReason string) (string, error) {
	current, err := a.sessions.Current(ctx, a.scope(ctx))
	if err != nil {
		return "", err
	}
	_, content, err := a.compactSession(ctx, current, triggerReason, a.modelSelectionForTurn(ctx, current))
	return content, err
}

func (a *Agent) compactSession(ctx context.Context, current *storage.Session, triggerReason string, fallback config.ModelSelection) (*storage.Session, string, error) {
	selection := a.contextRuntime.compactSelection(fallback)
	next, err := a.contextRuntime.compactSession(ctx, current, a.scope(ctx), triggerReason, selection)
	if err != nil {
		return nil, "", err
	}
	return next, fmt.Sprintf("上下文压缩完成。\nnew session: %s", next.ID), nil
}

func (r *contextRuntimeState) compactSession(ctx context.Context, current *storage.Session, scope session.Scope, triggerReason string, selection config.ModelSelection) (*storage.Session, error) {
	if len(r.requests.ListBySession(current.ID)) > 0 {
		return nil, fmt.Errorf("当前会话有正在运行的请求，无法压缩")
	}
	_, reqCtx, done, err := r.requests.Start(ctx, request.StartRequest{SessionID: current.ID, Kind: request.KindCompress, Label: "compact"})
	if err != nil {
		return nil, err
	}
	lifecycleClosed := false
	turnStarted := false
	closeLifecycle := func() {
		if lifecycleClosed {
			return
		}
		if turnStarted {
			r.turns.CompleteCompact(current.ID)
		}
		done()
		lifecycleClosed = true
	}
	defer closeLifecycle()
	if err := reqCtx.Err(); err != nil {
		return nil, err
	}
	if !r.turns.StartCompact(current.ID) {
		return nil, fmt.Errorf("当前会话正在处理其他任务，无法压缩")
	}
	turnStarted = true
	if err := reqCtx.Err(); err != nil {
		return nil, err
	}

	loaded, err := r.load(reqCtx, current.ID)
	if err != nil {
		return nil, err
	}
	if len(loaded.Messages) == 0 {
		return nil, fmt.Errorf("没有可压缩的历史消息")
	}
	rawMessages, err := r.loadRawMessages(reqCtx, current.ID)
	if err != nil {
		return nil, err
	}
	compactMessages, err := r.compactMessages(reqCtx, loaded)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	compressor := r.compressor
	r.mu.Unlock()
	result, err := compressor.Compact(reqCtx, contextmgr.CompactRequest{
		Provider:   selection.Provider,
		Model:      selection.Model,
		Messages:   compactMessages,
		UserInputs: compactUserInputs(rawMessages),
	})
	if err != nil {
		return nil, err
	}
	fromMessageID := loaded.Messages[0].ID
	if loaded.Summary != nil && loaded.Summary.FromMessageID != "" {
		fromMessageID = loaded.Summary.FromMessageID
	}
	title, generation, baseTitle := nextCompactedTitle(current)
	metadata := sessionMetadata{
		ContextCompact: &contextCompactState{
			Pending:         true,
			Summary:         result.AssembledSummary,
			SourceSessionID: current.ID,
			FromMessageID:   fromMessageID,
			ToMessageID:     loaded.Messages[len(loaded.Messages)-1].ID,
			Provider:        selection.Provider,
			Model:           selection.Model,
			TriggerReason:   triggerReason,
			Generation:      generation,
			BaseTitle:       baseTitle,
		},
		TitleRenamed: true,
		TitleSource:  "compact",
	}
	if result.Usage != nil {
		metadata.ContextCompact.SourceTokens = result.Usage.PromptTokens
		metadata.ContextCompact.SummaryTokens = result.Usage.CompletionTokens
		metadata.ContextCompact.TotalTokens = result.Usage.TotalTokens
		metadata.ContextCompact.CacheHitTokens = result.Usage.CacheHitTokens
	}
	next, err := r.sessions.Create(reqCtx, scope, session.CreateRequest{
		Title:    title,
		Mode:     current.Mode,
		Metadata: encodeSessionMetadata(metadata),
	})
	if err != nil {
		return nil, err
	}
	closeLifecycle()
	return next, nil
}

func nextCompactedTitle(source *storage.Session) (title string, generation int, baseTitle string) {
	baseTitle = strings.TrimSpace(source.Title)
	metadata := decodeSessionMetadata(source.Metadata)
	if compact := metadata.ContextCompact; compact != nil && compact.Generation > 0 {
		generation = compact.Generation
		expected := formatCompactedTitle(compact.BaseTitle, compact.Generation)
		if source.Title == expected {
			baseTitle = compact.BaseTitle
		}
	}
	if baseTitle == "" {
		baseTitle = "New session"
	}
	generation++
	return formatCompactedTitle(baseTitle, generation), generation, baseTitle
}

func formatCompactedTitle(baseTitle string, generation int) string {
	return fmt.Sprintf("%s compacted-%d", strings.TrimSpace(baseTitle), generation)
}

func (r *contextRuntimeState) compactMessages(ctx context.Context, loaded *contextmgr.LoadedContext) ([]contextmgr.CompactMessage, error) {
	callIDs := []string{}
	for _, message := range loaded.Messages {
		if message.Role != storage.RoleAssistant {
			continue
		}
		for _, call := range assistantMessageMetadata(message.Metadata).ToolCalls {
			if call.ID != "" {
				callIDs = append(callIDs, call.ID)
			}
		}
	}
	successful := map[string]bool{}
	if len(callIDs) > 0 {
		if r.store == nil || r.store.ToolCalls() == nil {
			return nil, fmt.Errorf("tool call repository is not configured")
		}
		var err error
		successful, err = r.store.ToolCalls().SuccessfulIDs(ctx, callIDs)
		if err != nil {
			return nil, err
		}
	}

	out := make([]contextmgr.CompactMessage, 0, len(loaded.Messages))
	summaryInjected := false
	for _, message := range loaded.Messages {
		switch message.Role {
		case storage.RoleUser:
			content := message.Content
			if loaded.Summary != nil && !summaryInjected {
				content = summaryUserPrefix(loaded.Summary.Summary) + content
				summaryInjected = true
			}
			if strings.TrimSpace(content) != "" {
				out = append(out, contextmgr.CompactMessage{Role: storage.RoleUser, Content: content})
			}
		case storage.RoleAssistant:
			metadata := assistantMessageMetadata(message.Metadata)
			content := message.Content
			if metadata.RawText != "" {
				content = metadata.RawText
			}
			calls := make([]contextmgr.CompactToolCall, 0, len(metadata.ToolCalls))
			for _, call := range metadata.ToolCalls {
				if !successful[call.ID] {
					continue
				}
				calls = append(calls, contextmgr.CompactToolCall{Name: call.Name, Arguments: call.Arguments})
			}
			if strings.TrimSpace(content) != "" || len(calls) > 0 {
				out = append(out, contextmgr.CompactMessage{Role: storage.RoleAssistant, Content: content, ToolCalls: calls})
			}
		}
	}
	if loaded.Summary != nil && !summaryInjected && strings.TrimSpace(loaded.Summary.Summary) != "" {
		out = append([]contextmgr.CompactMessage{{Role: storage.RoleUser, Content: loaded.Summary.Summary}}, out...)
	}
	return out, nil
}

func compactUserInputs(messages []storage.Message) []string {
	inputs := []string{}
	for _, message := range messages {
		if message.Role == storage.RoleUser && strings.TrimSpace(message.Content) != "" {
			inputs = append(inputs, message.Content)
		}
	}
	return inputs
}

func (r *contextRuntimeState) compactActive(sessionID string) bool {
	if r.turns.Snapshot(sessionID).Phase == turn.PhaseCompact {
		return true
	}
	for _, active := range r.requests.ListBySession(sessionID) {
		if active.Kind == request.KindCompress {
			return true
		}
	}
	return false
}

func (a *Agent) compactActive(sessionID string) bool {
	return a.contextRuntime.compactActive(sessionID)
}
