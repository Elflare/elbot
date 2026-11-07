package turn

import (
	"context"
	"elbot/internal/testutil"
	"testing"
	"testing/synctest"
	"time"
)

// Inject refresh after the timer fires but before expiry commits.
func TestRefreshWinsPendingExpiry(t *testing.T) {
	t.Run("risk", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := NewSupervisor(testutil.IDSequence())
			s.StartAttempt("s", Input{}, NewExecution("run"), "a")
			done := make(chan bool, 1)
			s.StartToolPhase("s", "a")
			w := s.BeginRiskConfirmation("s", RiskConfirmation{ToolName: "call"}, "a")
			expire := w.wait.Expire
			first := true
			w.wait.Expire = func() bool {
				if first {
					first = false
					s.RefreshRiskConfirmation("s")
				}
				return expire()
			}
			go func() { done <- w.Wait(t.Context(), time.Second).Outcome == RiskExpired }()
			synctest.Wait()
			time.Sleep(time.Second)
			synctest.Wait()
			select {
			case <-done:
				t.Fatal("accepted refresh lost to old timer")
			default:
			}
			time.Sleep(time.Second)
			if !<-done || s.Snapshot("s").Phase != PhaseStopping {
				t.Fatal("refreshed wait did not expire")
			}
			s.FinishAttempt("s", "a")
		})
	})
}

func TestSupervisorAppendAndAttemptOwnership(t *testing.T) {
	s := NewSupervisor(testutil.IDSequence())
	e := NewExecution("run")
	if !s.StartAttempt("s", Input{Text: "first"}, e, "old") {
		t.Fatal("start failed")
	}
	if !s.BeginAppend("s", "old", Input{Text: "more"}) {
		t.Fatal("append registration failed")
	}
	snapshot := s.Snapshot("s")
	if snapshot.Execution != e || snapshot.Attempt != "old" || snapshot.Phase != PhaseAwaitAppendConfirm {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	s.FinishAttempt("s", "old")
	input, resumed, ok := s.ResumeAppend("s")
	if !ok || resumed != e || !s.StartAttempt("s", input, e, "new") {
		t.Fatal("continuation failed")
	}
	s.StopAttempt("s", "old")
	s.FinishAttempt("s", "old")
	if s.Snapshot("s").Attempt != "new" {
		t.Fatal("old attempt affected replacement")
	}
	s.StopSession("s")
	if s.Snapshot("s").Phase != PhaseStopping {
		t.Fatal("stop released active work")
	}
	s.FinishAttempt("s", "new")
}

func TestRiskCallbacksKeepReplacementAndRejectStoppedApproval(t *testing.T) {
	s := NewSupervisor(testutil.IDSequence())
	s.StartAttempt("s", Input{}, NewExecution("run"), "a")
	s.StartToolPhase("s", "a")
	first := s.BeginRiskConfirmation("s", RiskConfirmation{ToolName: "first"}, "a")
	s.ResolveRiskConfirmation("s", RiskDecision{Action: RiskConfirm})
	if first.Wait(t.Context(), 0).Outcome != RiskAnswered {
		t.Fatal("early response lost")
	}
	second := s.BeginRiskConfirmation("s", RiskConfirmation{ToolName: "second"}, "a")
	if first.Cancel() {
		t.Fatal("old callback canceled replacement")
	}
	if pending, ok := s.PendingRiskConfirmation("s"); !ok || pending.ToolName != "second" {
		t.Fatal("replacement missing")
	}
	s.ResolveRiskConfirmation("s", RiskDecision{Action: RiskConfirm})
	s.StopAttempt("s", "a")
	if result := second.Wait(t.Context(), 0); result.Outcome != RiskCanceled {
		t.Fatalf("stopped approval: %+v", result)
	}
	if s.Snapshot("s").Phase != PhaseStopping {
		t.Fatal("active attempt released early")
	}
	s.FinishAttempt("s", "a")
}

func TestAppendReservationSurvivesOldAttemptExit(t *testing.T) {
	s := NewSupervisor(testutil.IDSequence())
	e := NewExecution("run")
	s.StartAttempt("s", Input{Text: "original"}, e, "a")
	if !s.BeginAppend("s", "a", Input{Text: "more"}) {
		t.Fatal("append transition failed")
	}
	s.FinishAttempt("s", "a")
	_, _, ok := s.ResumeAppend("s")
	if !ok {
		t.Fatal("resume failed")
	}
	if !s.BeginAppend("s", "", Input{Text: "latest"}) || s.Snapshot("s").Phase != PhaseAwaitAppendConfirm {
		t.Fatal("reserved append did not accept further input")
	}
	s.StopSession("s")
	if s.Snapshot("s").Phase != PhaseIdle {
		t.Fatal("stop did not release reserved append")
	}
	s.StartAttempt("s", Input{}, e, "b")
	s.StartToolPhase("s", "b")
	risk := s.BeginRiskConfirmation("s", RiskConfirmation{ToolName: "risk"}, "b")
	if result := risk.Wait(t.Context(), time.Millisecond); result.Outcome != RiskExpired {
		t.Fatalf("risk expiry: %+v", result)
	}
	s.FinishAttempt("s", "b")
}

func TestSupervisorRiskDecisionAndTermination(t *testing.T) {
	for _, action := range []RiskAction{RiskConfirm, RiskConfirmTool, RiskConfirmAll, RiskReject} {
		t.Run(string(action), func(t *testing.T) {
			s := NewSupervisor(testutil.IDSequence())
			s.StartAttempt("s", Input{}, NewExecution("run"), "a")
			s.StartToolPhase("s", "a")
			wait := s.BeginRiskConfirmation("s", RiskConfirmation{ToolName: "call"}, "a")
			decision := RiskDecision{Action: action, Extra: "extra", Reason: "reason"}
			if s.ResolveRiskConfirmation("s", RiskDecision{}) || !s.ResolveRiskConfirmation("s", decision) {
				t.Fatal("decision validation failed")
			}
			result := wait.Wait(t.Context(), 0)
			if result.Outcome != RiskAnswered || result.Decision != decision || result.Err != nil {
				t.Fatalf("result = %+v", result)
			}
			wait = s.BeginRiskConfirmation("s", RiskConfirmation{ToolName: "next"}, "a")
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			result = wait.Wait(ctx, 0)
			if result.Outcome != RiskCanceled || result.Err != context.Canceled {
				t.Fatalf("cancellation = %+v", result)
			}
			s.FinishAttempt("s", "a")
		})
	}
	var wait *RiskConfirmationWait
	if result := wait.Wait(t.Context(), 0); result.Outcome != RiskStale {
		t.Fatalf("absent wait = %+v", result)
	}
}
