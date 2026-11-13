package cron

import (
	"elbot/internal/config"
	"elbot/internal/operationresult"
)

func (s *Service) modelForTask(meta CronLLMMetadata) operationresult.Result[config.ModelSelection] {
	if meta.ModelProvider == "" && meta.Model == "" {
		return operationresult.Result[config.ModelSelection]{Status: operationresult.Success, Data: s.models.ResolveMode("work").ModelSelection}
	}
	if meta.ModelProvider == "" || meta.Model == "" {
		return operationresult.Result[config.ModelSelection]{Status: operationresult.InvalidArguments, Message: "model_provider and model must both be set or cleared"}
	}
	if result := s.models.ValidateSelection(config.ModelSelection{Provider: meta.ModelProvider, Model: meta.Model}); result.Status != operationresult.Success {
		return operationresult.Result[config.ModelSelection]{Status: result.Status, Message: result.Message}
	}
	return operationresult.Result[config.ModelSelection]{Status: operationresult.Success, Data: config.ModelSelection{Provider: meta.ModelProvider, Model: meta.Model}}
}
