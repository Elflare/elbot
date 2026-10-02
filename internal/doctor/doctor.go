// Package doctor presents read-only configuration inspection results.
package doctor

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"elbot/internal/config"
)

type Inspector interface {
	Inspect(context.Context, string) (config.Inspection, error)
}

type Service struct {
	configPath string
	inspector  Inspector
}

func New(configPath string, inspector Inspector) (*Service, error) {
	if strings.TrimSpace(configPath) == "" {
		return nil, fmt.Errorf("doctor: main config path is required")
	}
	if inspector == nil {
		return nil, fmt.Errorf("doctor: config inspector is required")
	}
	path, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("doctor: resolve config path: %w", err)
	}
	return &Service{configPath: path, inspector: inspector}, nil
}

func (s *Service) Check(ctx context.Context) (Report, error) {
	report := Report{ConfigPath: s.configPath}
	result, err := s.inspector.Inspect(ctx, s.configPath)
	if err != nil {
		return report, err
	}
	for _, issue := range result.Issues {
		report.add(issue)
	}
	return report, nil
}
