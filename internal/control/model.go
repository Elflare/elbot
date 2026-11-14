package control

import (
	"context"
	"slices"
	"strings"

	"elbot/internal/config"
	"elbot/internal/control/share"
	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func (m *Supervisor) ListModels(ctx context.Context, req ListModelsRequest) (operationresult.Result[ListModelsResult], error) {
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[ListModelsResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[ListModelsResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	return operationresult.Result[ListModelsResult]{Status: operationresult.Success, Data: m.modelList(req.Query, false)}, nil
}

func (m *Supervisor) RefreshModels(ctx context.Context, req ListModelsRequest) (operationresult.Result[RefreshModelsResult], error) {
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[RefreshModelsResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[RefreshModelsResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	if err := m.deps.Models.ReloadProviders(ctx); err != nil {
		return operationresult.Result[RefreshModelsResult]{}, err
	}
	return operationresult.Result[RefreshModelsResult]{Status: operationresult.Success, Data: m.modelList(req.Query, true)}, nil
}

func (m *Supervisor) SelectModel(ctx context.Context, req SelectModelRequest) (operationresult.Result[SelectModelResult], error) {
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[SelectModelResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[SelectModelResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	if strings.TrimSpace(req.Provider) == "" || strings.TrimSpace(req.Model) == "" || strings.Contains(req.Provider, "/") {
		return operationresult.Result[SelectModelResult]{Status: operationresult.InvalidArguments, Message: "需要明确的 provider 和 model"}, nil
	}
	if !slices.Contains([]string{"current", "chat", "work", "compact", "naming", "elwisp1", "elwisp2", "elwisp3"}, req.Target) {
		return operationresult.Result[SelectModelResult]{Status: operationresult.InvalidArguments, Message: "未知模型选择目标"}, nil
	}
	arg := req.Provider + "/" + req.Model
	var result operationresult.Result[modelmgr.ModelOption]

	switch req.Target {
	case "compact":
		result, err = m.deps.Models.SelectCompactModel(arg)
	case "naming":
		result, err = m.deps.Models.SelectNamingModel(arg)
	case "elwisp1", "elwisp2", "elwisp3":
		result, err = m.deps.Models.PrepareModel(arg)
		if err == nil && result.Status == operationresult.Success {
			result, err = m.deps.Models.CommitModelForMode(req.Target, result.Data)
		}
	default:
		currentResult, currentErr := m.deps.Sessions.Current(ctx, m.deps.Scope(ctx))
		if currentErr != nil {
			return operationresult.Result[SelectModelResult]{}, currentErr
		}
		mode := m.deps.Sessions.DefaultMode()
		var current *storage.Session
		if currentResult.Status == operationresult.Success {
			current = currentResult.Data
			mode = current.Mode
			if mode == storage.SessionModeBackground {
				mode = storage.SessionModeWork
			}
		} else if currentResult.Status != operationresult.NoCurrentSession {
			return operationresult.Result[SelectModelResult]{Status: currentResult.Status, Message: currentResult.Message}, nil
		}
		currentMode := mode
		if req.Target != "current" {
			mode = req.Target
		}
		result, err = m.deps.Models.PrepareModel(arg)
		if err != nil {
			return operationresult.Result[SelectModelResult]{}, err
		}
		if result.Status != operationresult.Success {
			return operationresult.Result[SelectModelResult]{Status: result.Status, Message: result.Message}, nil
		}
		selected := result.Data
		if current != nil && mode == currentMode {
			origin, known, originErr := session.Origin(current)
			if originErr != nil {
				return operationresult.Result[SelectModelResult]{}, originErr
			}
			if known {
				compatible, err := modelmgr.CanSwitch(origin, m.deps.Models.Resolve(config.ModelSelection{Provider: selected.Provider, Model: selected.Model}).Origin)
				if err != nil {
					return operationresult.Result[SelectModelResult]{}, err
				}
				if compatible.Status != operationresult.Success {
					return operationresult.Result[SelectModelResult]{Status: compatible.Status, Message: compatible.Message}, nil
				}
			}
		}
		result, err = m.deps.Models.CommitModelForMode(mode, selected)
	}
	if err != nil {
		return operationresult.Result[SelectModelResult]{}, err
	}
	if result.Status != operationresult.Success {
		return operationresult.Result[SelectModelResult]{Status: result.Status, Message: result.Message}, nil
	}
	return operationresult.Result[SelectModelResult]{Status: operationresult.Success, Data: SelectModelResult{Target: req.Target, Model: modelData(result.Data)}}, nil
}

func (m *Supervisor) modelList(query string, fresh bool) ListModelsResult {
	listed := m.deps.Models.ModelList(query, modelmgr.ModelListOptions{Fresh: fresh})
	result := ListModelsResult{Query: query, Items: []ModelData{}, Errors: []ItemError{}}
	for _, item := range listed.Options {
		result.Items = append(result.Items, modelData(item))
	}
	for _, item := range listed.Errors {
		result.Errors = append(result.Errors, ItemError{ID: item.Provider, Error: item.Err.Error()})
	}
	return result
}

func modelData(item modelmgr.ModelOption) ModelData {
	return ModelData{Provider: item.Provider, Model: item.Model, Index: item.Index, Modes: slices.Clone(item.ModeMarks), Compact: item.Compact, Naming: item.Naming}
}
