package agent

import (
	"context"
	"errors"
	"slices"

	agentexecution "elbot/internal/agent/execution"
	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/control"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// controlAdapter 只向统一操作提供需要的能力；组件仍由 Agent 根部持有。
type controlAdapter struct {
	supervisor *Supervisor
	store      storage.Store
	models     ModelCapabilities
	scope      func(context.Context) session.Scope
	status     *agentexecution.StatusRecorder
}

func (a controlAdapter) GetSession(ctx context.Context, id string) (*storage.Session, error) {
	return a.store.Sessions().Get(ctx, id)
}
func (a controlAdapter) LoadMessagePage(ctx context.Context, id string, page, size int) (contextmgr.MessagePage, error) {
	return (contextmgr.Loader{Store: a.store}).LoadMessagePage(ctx, id, page, size)
}
func (a controlAdapter) SnapshotAll() []turn.Snapshot { return a.supervisor.turns.SnapshotAll() }
func (a controlAdapter) CompactCurrent(ctx context.Context, id string) (*storage.Session, error) {
	return a.supervisor.execution.CompactCurrent(ctx, "manual")
}
func (a controlAdapter) StopSession(id string) { a.supervisor.turns.StopSession(id) }
func (a controlAdapter) Commands(context.Context) ([]control.CommandInfo, error) {
	items := []control.CommandInfo{}
	for _, info := range a.supervisor.Commands() {
		items = append(items, control.CommandInfo{Name: info.Name, Args: info.Args, Aliases: slices.Clone(info.Aliases), Description: info.Description, Help: info.Help, MinRole: info.MinRole})
	}
	return items, nil
}

func (a controlAdapter) Status(ctx context.Context) (control.StatusResult, error) {
	result := control.StatusResult{Requests: []control.RequestData{}, ToolUsage: map[string]int{}, Errors: []control.ItemError{}, Phase: turn.PhaseIdle}
	scope := a.scope(ctx)
	locked, release, err := a.supervisor.sessions.EnterActivation(ctx, scope)
	if err != nil {
		return result, err
	}
	defer release()
	state, err := a.supervisor.sessions.Status(locked, scope)
	if err != nil && !errors.Is(err, session.ErrNoCurrentSession) {
		return result, err
	}
	mode := a.supervisor.sessions.DefaultMode()
	var row *storage.Session
	if state != nil {
		row = state.Session
		mode = row.Mode
		result.Session = &control.SessionData{ID: row.ID, Title: row.Title, Mode: row.Mode, Status: row.Status, Platform: row.Platform, ChatID: row.ChatID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ArchivedAt: row.ArchivedAt, PinnedAt: row.PinnedAt, ParentSessionID: row.ParentSessionID, ForkFromMessageID: row.ForkFromMessageID}
		result.MessageCount, result.LastUserContent, result.LastAssistantContent = state.MessageCount, state.LastUserContent, state.LastAnswerContent
		snapshot := a.supervisor.turns.Snapshot(row.ID)
		result.Phase, result.PendingCount, result.TurnToolUsage = snapshot.Phase, snapshot.PendingCount, snapshot.Tools
		for _, request := range a.supervisor.requests.ListBySession(row.ID) {
			result.Requests = append(result.Requests, control.RequestData{ID: request.ID, ParentID: request.ParentID, SessionID: request.SessionID, SessionTitle: row.Title, Kind: request.Kind, Label: request.Label, StartedAt: request.StartedAt, Stopping: request.Stopping})
		}
		usage, usageErr := a.store.ToolCalls().UsageBySession(locked, row.ID)
		if usageErr != nil {
			result.ToolUsage = nil
			result.Errors = append(result.Errors, controlFailure("tool_usage", usageErr))
		} else {
			for _, entry := range usage {
				result.ToolUsage[entry.ToolName] += entry.Count
			}
		}
	} else {
		result.DefaultMode, result.Platform, result.ChatID = mode, scope.Platform, scope.ChatID
		result.CompactModel = controlModel(a.models.CurrentCompactModel(mode))
	}
	model := a.models.CurrentModelForMode(mode)
	result.Mode, result.Model = mode, controlModel(model)
	usage, usageErr := a.status.Usage(row)
	if usageErr != nil {
		result.Errors = append(result.Errors, controlFailure("usage", usageErr))
	}
	stateContext := a.supervisor.contexts.Status(locked, usage, config.ModelSelection{Provider: model.Provider, Model: model.Model})
	result.Context = control.ContextData{Usage: stateContext.Usage, ContextWindow: stateContext.ContextWindow, CompactEnabled: stateContext.CompactEnabled, CompactTriggerRatio: stateContext.TriggerRatio}
	return result, nil
}
func controlModel(value modelmgr.ModelOption) control.ModelData {
	return control.ModelData{Provider: value.Provider, Model: value.Model, Index: value.Index, Modes: slices.Clone(value.ModeMarks), Compact: value.Compact, Naming: value.Naming}
}
func controlFailure(id string, err error) control.ItemError {
	failure, _ := control.ClassifyError(err).(*control.Error)
	return control.ItemError{ID: id, Error: failure}
}
