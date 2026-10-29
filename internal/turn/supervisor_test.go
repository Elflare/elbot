package turn

import (
	"context"
	"testing"
	"time"
)

func TestSupervisorAppendAndAttemptOwnership(t *testing.T) {
	s := NewSupervisor()
	e := NewExecution("run")
	if !s.StartAttempt("s", Input{Text: "first"}, e, "old") {
		t.Fatal("start failed")
	}
	wait := s.BeginAppend("s", "old", Input{Text: "more"})
	if wait == nil {
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
	if wait.Wait(t.Context(), 0) {
		t.Fatal("resolved append wait expired")
	}
	s.StopSession("s")
	if s.Snapshot("s").Phase != PhaseStopping {
		t.Fatal("stop released active work")
	}
	s.FinishAttempt("s", "new")
}

func TestRiskCallbacksKeepReplacementAndRejectStoppedApproval(t *testing.T) {
	s := NewSupervisor()
	s.StartAttempt("s", Input{}, NewExecution("run"), "a")
	s.StartToolPhase("s", "a")
	first := s.BeginRiskConfirmation("s", RiskConfirmation{ID: "first"}, "a")
	s.ResolveRiskConfirmation("s", RiskDecision{Action: RiskConfirm})
	if first.Wait(t.Context(), 0).Outcome != RiskAnswered {
		t.Fatal("early response lost")
	}
	second := s.BeginRiskConfirmation("s", RiskConfirmation{ID: "second"}, "a")
	if first.Cancel() {
		t.Fatal("old callback canceled replacement")
	}
	if pending, ok := s.PendingRiskConfirmation("s"); !ok || pending.ID != "second" {
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

func TestConfirmationCallbacksExpireAndIgnoreOldAppend(t *testing.T) {
	s := NewSupervisor()
	e := NewExecution("run")
	s.StartAttempt("s", Input{Text: "original"}, e, "a")
	old := s.BeginAppend("s", "a", Input{Text: "more"})
	s.FinishAttempt("s", "a")
	_, _, ok := s.ResumeAppend("s")
	if !ok {
		t.Fatal("resume failed")
	}
	replacement := s.BeginAppend("s", "", Input{Text: "latest"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if old.Wait(ctx, 0) || s.Snapshot("s").Phase != PhaseAwaitAppendConfirm {
		t.Fatal("old cancellation changed replacement")
	}
	if !replacement.Wait(t.Context(), time.Millisecond) || s.Snapshot("s").Phase != PhaseIdle {
		t.Fatal("append expiry failed")
	}
	s.StartAttempt("s", Input{}, e, "b")
	s.StartToolPhase("s", "b")
	risk := s.BeginRiskConfirmation("s", RiskConfirmation{ID: "risk"}, "b")
	if result := risk.Wait(t.Context(), time.Millisecond); result.Outcome != RiskExpired {
		t.Fatalf("risk expiry: %+v", result)
	}
	s.FinishAttempt("s", "b")
}

func TestSupervisorRiskDecisionAndTermination(t *testing.T) {
	for _, action := range []RiskAction{RiskConfirm, RiskConfirmTool, RiskConfirmAll, RiskReject} {
		t.Run(string(action), func(t *testing.T) {
			s := NewSupervisor()
			s.StartAttempt("s", Input{}, NewExecution("run"), "a")
			s.StartToolPhase("s", "a")
			wait := s.BeginRiskConfirmation("s", RiskConfirmation{ID: "call"}, "a")
			decision := RiskDecision{Action: action, Extra: "extra", Reason: "reason"}
			if s.ResolveRiskConfirmation("s", RiskDecision{}) || !s.ResolveRiskConfirmation("s", decision) {
				t.Fatal("decision validation failed")
			}
			result := wait.Wait(t.Context(), 0)
			if result.Outcome != RiskAnswered || result.Decision != decision || result.Err != nil {
				t.Fatalf("result = %+v", result)
			}
			wait = s.BeginRiskConfirmation("s", RiskConfirmation{ID: "next"}, "a")
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
