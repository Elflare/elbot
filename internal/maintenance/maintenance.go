package maintenance

import (
	"context"
	"log/slog"
	"time"

	"elbot/internal/config"
	"elbot/internal/logging"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type Service struct {
	logs           *logging.Manager
	store          storage.Store
	sessionCleanup config.SessionCleanupConfig
	logger         *slog.Logger
}

func NewService(logs *logging.Manager, store storage.Store, sessionCleanup config.SessionCleanupConfig, logger *slog.Logger) *Service {
	return &Service{logs: logs, store: store, sessionCleanup: sessionCleanup, logger: logger}
}

func (s *Service) RunLogCleanup(ctx context.Context) error {
	if err := s.logs.CleanupOldLogs(); err != nil {
		s.warn("maintenance log cleanup failed", "error", err)
		return err
	}
	s.info("maintenance log cleanup completed", "retention_days", s.logs.RetentionDays())
	return nil
}

func (s *Service) RunSessionCleanup(ctx context.Context) error {
	if !s.sessionCleanup.Enabled {
		return nil
	}
	deleted, err := session.NewService(s.store).CleanupExpired(ctx, time.Now().AddDate(0, 0, -s.sessionCleanup.RetentionDays))
	if err != nil {
		s.warn("maintenance session cleanup failed", "error", err, "retention_days", s.sessionCleanup.RetentionDays)
		return err
	}
	s.info("maintenance session cleanup completed", "deleted", deleted, "retention_days", s.sessionCleanup.RetentionDays)
	return nil
}

func (s *Service) info(msg string, attrs ...any) {
	if s.logger != nil {
		s.logger.Info(msg, attrs...)
	}
}

func (s *Service) warn(msg string, attrs ...any) {
	if s.logger != nil {
		s.logger.Warn(msg, attrs...)
	}
}
