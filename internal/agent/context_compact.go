package agent

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
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

func (a *Agent) compactSession(ctx context.Context, current *storage.Session, triggerReason string, fallback modelmgr.Selection) (*storage.Session, string, error) {
	selection := a.models.ResolveCompact(fallback)
	next, err := a.contextRuntime.compactSession(ctx, current, a.scope(ctx), triggerReason, selection)
	if err != nil {
		return nil, "", err
	}
	return next, fmt.Sprintf("上下文压缩完成。\nnew session: %s", next.ID), nil
}

func (r *contextRuntimeState) compactSession(ctx context.Context, current *storage.Session, scope session.Scope, triggerReason string, selection modelmgr.Selection) (*storage.Session, error) {
	locked, scope, release, err := r.enterCompact(ctx, current, scope)
	if err != nil {
		return nil, err
	}
	if len(r.requests.ListBySession(current.ID)) > 0 {
		release()
		return nil, fmt.Errorf("当前会话有正在运行的请求，无法压缩")
	}
	info, reqCtx, done, err := r.requests.Start(locked, request.StartRequest{SessionID: current.ID, Kind: request.KindCompress, Label: "compact"})
	if err != nil {
		release()
		return nil, err
	}
	if !r.turns.StartCompactRun(current.ID, info.ID, turn.ExecutionFromContext(ctx)) {
		done()
		release()
		return nil, fmt.Errorf("当前会话正在处理其他任务，无法压缩")
	}
	r.turns.AttachExecution(current.ID, info.ID, turn.ExecutionFromContext(ctx))
	release()
	defer done()
	defer r.turns.CompleteCompactRun(current.ID, info.ID)
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
	compressor.ClientFor = func(string) llm.LLM { return selection.Client }
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
	nextID := storage.NewID()
	locked, scope, release, err = r.enterCompact(reqCtx, current, scope, nextID)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := reqCtx.Err(); err != nil {
		return nil, err
	}

	if !r.turns.CompleteCompactRun(current.ID, info.ID) {
		return nil, context.Canceled
	}
	// Carry field-owned metadata (including workspace and permanent promotion)
	// forward while replacing the compression seed.
	fields, err := storage.DecodeSessionMetadata(current.Metadata)
	if err != nil {
		return nil, err
	}
	seed, err := storage.DecodeSessionMetadata(encodeSessionMetadata(metadata))
	if err != nil {
		return nil, err
	}
	for key, value := range seed {
		fields[key] = value
	}
	delete(fields, "last_usage")
	encoded, err := fields.Encode()
	if err != nil {
		return nil, err
	}
	var next *storage.Session
	if session.IsBackground(current) {
		next = &storage.Session{ID: nextID, OwnerID: current.OwnerID, Platform: current.Platform, PlatformScopeID: current.PlatformScopeID, Title: title, Mode: current.Mode, Status: storage.SessionStatusActive, Metadata: encoded}
		err = r.store.Sessions().Create(locked, next)
	} else {
		next, err = r.sessions.Create(locked, scope, session.CreateRequest{ID: nextID, Title: title, Mode: current.Mode, Metadata: encoded})
	}
	if err != nil {
		return nil, err
	}
	if e := turn.ExecutionFromContext(ctx); e != nil {
		e.SetResult(next.ID, "", "")
		if !r.turns.ReserveExecution(next.ID, inboundTurnInput(ctx, ""), e) {
			return nil, session.ErrSessionBusy
		}
	}
	if e := turn.ExecutionFromContext(ctx); e != nil && e.Foreground() != nil {
		_, nextBinding, err := r.sessions.CurrentBound(locked, scope)
		if err != nil {
			return nil, err
		}
		e.Adopt(session.WithBinding(e.Foreground(), nextBinding))
	}
	return next, nil
}

func (r *contextRuntimeState) enterCompact(ctx context.Context, row *storage.Session, scope session.Scope, targets ...string) (context.Context, session.Scope, func(), error) {
	for {
		if e := turn.ExecutionFromContext(ctx); e != nil && e.Foreground() != nil {
			if binding, ok := session.BindingFromContext(e.Foreground()); ok {
				ctx = session.WithBinding(ctx, binding)
				scope = binding.Scope()
			}
		}
		background := session.IsBackground(row)
		var locked context.Context
		var release func()
		var err error
		if background {
			locked, release, err = r.sessions.EnterSessions(ctx, append([]string{row.ID}, targets...)...)
		} else {
			locked, release, err = r.sessions.EnterActivation(ctx, scope, append([]string{row.ID}, targets...)...)
		}
		if err != nil {
			return ctx, scope, nil, err
		}
		latest, err := r.store.Sessions().Get(locked, row.ID)
		if err != nil {
			release()
			return ctx, scope, nil, err
		}
		*row = *latest
		if background && !session.IsBackground(row) {
			release()
			continue
		}
		if !background {
			_, binding, err := r.sessions.CurrentBound(locked, scope)
			if err != nil {
				release()
				return ctx, scope, nil, err
			}
			original, hasOriginal := session.BindingFromContext(ctx)
			if binding.SessionID() != row.ID || (hasOriginal && (original != binding || !original.Valid())) {
				release()
				return ctx, scope, nil, errSessionBindingChanged
			}
			locked = session.WithBinding(locked, binding)
		}
		return locked, scope, release, nil
	}
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
