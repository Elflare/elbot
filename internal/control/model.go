package control

import (
	"context"
	"errors"
	"slices"
	"strings"

	"elbot/internal/config"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func (m *Supervisor) ListModels(ctx context.Context, req ListModelsRequest) (ListModelsResult, error) {
	if err := authorize(ctx, true, m.deps.Models != nil); err != nil {
		return ListModelsResult{}, err
	}
	return m.modelList(req.Query, false), nil
}

func (m *Supervisor) RefreshModels(ctx context.Context, req ListModelsRequest) (RefreshModelsResult, error) {
	if err := authorize(ctx, true, m.deps.Models != nil); err != nil {
		return RefreshModelsResult{}, err
	}
	if err := m.deps.Models.ReloadProviders(ctx); err != nil {
		return RefreshModelsResult{}, ClassifyError(err)
	}
	return m.modelList(req.Query, true), nil
}

func (m *Supervisor) SelectModel(ctx context.Context, req SelectModelRequest) (SelectModelResult, error) {
	if err := authorize(ctx, true, m.deps.Models != nil); err != nil {
		return SelectModelResult{}, err
	}
	if strings.TrimSpace(req.Provider) == "" || strings.TrimSpace(req.Model) == "" || strings.Contains(req.Provider, "/") {
		return SelectModelResult{}, invalid("需要明确的 provider 和 model")
	}
	if !slices.Contains([]string{"current", "chat", "work", "compact", "naming", "elwisp1", "elwisp2", "elwisp3"}, req.Target) {
		return SelectModelResult{}, invalid("未知模型选择目标")
	}
	arg := req.Provider + "/" + req.Model
	var selected modelmgr.ModelOption
	var err error
	switch req.Target {
	case "compact":
		selected, err = m.deps.Models.SelectCompactModel(arg)
	case "naming":
		selected, err = m.deps.Models.SelectNamingModel(arg)
	case "elwisp1", "elwisp2", "elwisp3":
		selected, err = m.deps.Models.PrepareModel(arg)
		if err == nil {
			selected, err = m.deps.Models.CommitModelForMode(req.Target, selected)
		}
	default:
		if m.deps.Sessions == nil {
			return SelectModelResult{}, &Error{Code: "not_configured", Message: "当前模型的会话上下文尚未接入"}
		}
		current, currentErr := m.deps.Sessions.Current(ctx, m.deps.Scope(ctx))
		if currentErr != nil && !errors.Is(currentErr, session.ErrNoCurrentSession) {
			return SelectModelResult{}, ClassifyError(currentErr)
		}
		mode := m.deps.Sessions.DefaultMode()
		if current != nil {
			mode = current.Mode
			if mode == storage.SessionModeBackground {
				mode = storage.SessionModeWork
			}
		}
		currentMode := mode
		if req.Target != "current" {
			mode = req.Target
		}
		selected, err = m.deps.Models.PrepareModel(arg)
		if err == nil && current != nil && mode == currentMode {
			origin, known, originErr := session.Origin(current)
			if originErr != nil {
				return SelectModelResult{}, ClassifyError(originErr)
			}
			if known {
				err = modelmgr.CanSwitch(origin, m.deps.Models.Resolve(config.ModelSelection{Provider: selected.Provider, Model: selected.Model}).Origin)
			}
		}
		if err == nil {
			selected, err = m.deps.Models.CommitModelForMode(mode, selected)
		}
	}
	return SelectModelResult{Target: req.Target, Model: modelData(selected)}, ClassifyError(err)
}

func (m *Supervisor) modelList(query string, fresh bool) ListModelsResult {
	listed := m.deps.Models.ModelList(query, modelmgr.ModelListOptions{Fresh: fresh})
	result := ListModelsResult{Query: query, Items: []ModelData{}, Errors: []ItemError{}}
	for _, item := range listed.Options {
		result.Items = append(result.Items, modelData(item))
	}
	for _, item := range listed.Errors {
		failure, _ := ClassifyError(item.Err).(*Error)
		result.Errors = append(result.Errors, ItemError{ID: item.Provider, Error: failure})
	}
	return result
}

func modelData(item modelmgr.ModelOption) ModelData {
	return ModelData{Provider: item.Provider, Model: item.Model, Index: item.Index, Modes: slices.Clone(item.ModeMarks), Compact: item.Compact, Naming: item.Naming}
}
