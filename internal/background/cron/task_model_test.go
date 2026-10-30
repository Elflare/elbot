package cron

import (
	"context"
	"fmt"
	"testing"

	background "elbot/internal/background/share"
	"elbot/internal/config"
	"elbot/internal/modelmgr"
)

type taskModels struct {
	current     config.ModelSelection
	resolutions int
}

func (m *taskModels) ResolveMode(mode string) modelmgr.Selection {
	if mode != "work" {
		panic("unexpected model slot: " + mode)
	}
	m.resolutions++
	return modelmgr.Selection{ModelSelection: m.current}
}
func (m *taskModels) ValidateSelection(s config.ModelSelection) error {
	if s.Provider != "provider" {
		return fmt.Errorf("unknown provider")
	}
	return nil
}

type changingModelRunner struct {
	fakeCronRunner
	models *taskModels
}

func (r *changingModelRunner) RunBackground(ctx context.Context, req background.RunRequest) (background.RunResult, error) {
	r.models.current.Model = "changed"
	return r.fakeCronRunner.RunBackground(ctx, req)
}
func TestCronModelSnapshotSurvivesFormatRetry(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		models := &taskModels{current: config.ModelSelection{Provider: "provider", Model: "initial"}}
		runner := &changingModelRunner{models: models, fakeCronRunner: fakeCronRunner{texts: []string{"invalid JSON", `{"completed":true,"need_report":false,"report":"ok"}`}}}
		svc := newTestCronService(t, Options{Store: newFakeCronRepo(), Models: models, Runner: runner})
		meta := Metadata{LLM: CronLLMMetadata{ToolListNames: []string{"shell"}}}
		want := "initial"
		if explicit {
			meta.LLM.ModelProvider, meta.LLM.Model = "provider", "explicit"
			want = "explicit"
		}
		if _, _, err := svc.runLLMReport(context.Background(), Job{Name: "models"}, meta, CronDeliveryState{}); err != nil {
			t.Fatal(err)
		}
		if len(runner.requests) != 2 {
			t.Fatalf("requests=%d", len(runner.requests))
		}
		for _, req := range runner.requests {
			if req.ModelProvider != "provider" || req.Model != want {
				t.Fatalf("model changed: %+v", req)
			}
		}
		if len(runner.requests[0].ToolListNames) != 1 || len(runner.requests[1].ToolListNames) != 0 || runner.requests[1].SessionID != "cron-session" {
			t.Fatal("retry did not reuse initial session tools")
		}
		if !explicit && models.resolutions != 1 {
			t.Fatalf("resolved %d times", models.resolutions)
		}
	}
}

func TestCronExecutionRejectsInvalidModelBeforeRunning(t *testing.T) {
	for _, pair := range []config.ModelSelection{{Provider: "provider"}, {Model: "model"}, {Provider: "missing", Model: "model"}} {
		runner := &fakeCronRunner{}
		svc := newTestCronService(t, Options{Models: &taskModels{}, Runner: runner})
		_, _, err := svc.runLLMReport(t.Context(), Job{Name: "model"}, Metadata{LLM: CronLLMMetadata{ModelProvider: pair.Provider, Model: pair.Model}}, CronDeliveryState{})
		if err == nil || runner.calls != 0 {
			t.Fatalf("model=%#v err=%v calls=%d", pair, err, runner.calls)
		}
	}
}
