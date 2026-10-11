package runtime

import (
	"testing"
	"time"
)

func TestSnapshotElapsedUsesFinishedAt(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := Snapshot{TurnStartedAt: start, StageStartedAt: start.Add(time.Second), FinishedAt: start.Add(65 * time.Second)}
	if got := s.Elapsed(start.Add(2 * time.Minute)); got != 65*time.Second {
		t.Fatal(got)
	}
	if got := s.StageElapsed(start.Add(2 * time.Minute)); got != 64*time.Second {
		t.Fatal(got)
	}
}
