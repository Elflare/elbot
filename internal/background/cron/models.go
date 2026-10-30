package cron

import (
	"fmt"

	"elbot/internal/config"
)

func (s *Service) modelForTask(meta CronLLMMetadata) (config.ModelSelection, error) {
	if meta.ModelProvider == "" && meta.Model == "" {
		return s.models.ResolveMode("work").ModelSelection, nil
	}
	if meta.ModelProvider == "" || meta.Model == "" {
		return config.ModelSelection{}, fmt.Errorf("model_provider and model must both be set or cleared")
	}
	if err := s.models.ValidateSelection(config.ModelSelection{Provider: meta.ModelProvider, Model: meta.Model}); err != nil {
		return config.ModelSelection{}, err
	}
	return config.ModelSelection{Provider: meta.ModelProvider, Model: meta.Model}, nil
}
