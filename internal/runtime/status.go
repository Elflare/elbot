package runtime

import (
	"time"

	"elbot/internal/llm"
	"elbot/internal/request"
)

type Phase string

const (
	PhaseIdle      Phase = "idle"
	PhasePreparing Phase = "preparing"
	PhaseLLM       Phase = "llm"
	PhaseTool      Phase = "tool"
	PhaseSending   Phase = "sending"
	PhaseDone      Phase = "done"
	PhaseError     Phase = "error"
)

type Snapshot struct {
	SessionID string
	Phase     Phase

	Provider string
	Model    string
	Mode     string

	RequestID string
	Kind      request.Kind
	Label     string
	ToolName  string

	TurnStartedAt  time.Time
	StageStartedAt time.Time
	FinishedAt     time.Time

	Usage *llm.Usage
	Error string
}

func (s Snapshot) Running() bool {
	return !s.StageStartedAt.IsZero() && s.FinishedAt.IsZero() && s.Phase != PhaseIdle && s.Phase != PhaseDone && s.Phase != PhaseError
}

func (s Snapshot) Elapsed(now time.Time) time.Duration {
	start := s.TurnStartedAt
	if start.IsZero() {
		start = s.StageStartedAt
	}
	if start.IsZero() {
		return 0
	}
	end := s.FinishedAt
	if end.IsZero() {
		end = now
	}
	if end.Before(start) {
		return 0
	}
	return end.Sub(start)
}

func (s Snapshot) StageElapsed(now time.Time) time.Duration {
	if s.StageStartedAt.IsZero() {
		return s.Elapsed(now)
	}
	end := s.FinishedAt
	if end.IsZero() {
		end = now
	}
	if end.Before(s.StageStartedAt) {
		return 0
	}
	return end.Sub(s.StageStartedAt)
}
