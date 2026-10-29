package turn

import (
	"context"
	"testing"
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
