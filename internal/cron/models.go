package cron

import (
	"fmt"

	"elbot/internal/config"
	"elbot/internal/storage"
)

func (s *Service) validateModel(meta CronLLMMetadata) error {
	if meta.ModelProvider == "" && meta.Model == "" {
		return nil
	}
	if meta.ModelProvider == "" || meta.Model == "" {
		return fmt.Errorf("model_provider and model must both be set or cleared")
	}
	return s.models.ValidateSelection(config.ModelSelection{Provider: meta.ModelProvider, Model: meta.Model})
}

func (s *Service) modelForTask(meta CronLLMMetadata) (config.ModelSelection, error) {
	if err := s.validateModel(meta); err != nil {
		return config.ModelSelection{}, err
	}
	if meta.ModelProvider != "" {
		return config.ModelSelection{Provider: meta.ModelProvider, Model: meta.Model}, nil
	}
	return s.models.ResolveMode(storage.SessionModeWork).ModelSelection, nil
}
