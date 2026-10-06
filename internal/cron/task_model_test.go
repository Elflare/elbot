package cron

import (
	"context"
	"fmt"
	"testing"
	"time"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
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

func TestCronTaskModelCRUD(t *testing.T) {
	ctx := context.Background()
	repo := newFakeCronRepo()
	svc := NewService(Options{Store: fakeCronStore{cron: repo}, Models: &taskModels{}})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:04:30") }
	actor := contextinfo.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: contextinfo.RoleSuperadmin}
	req := UpsertRequest{Name: "models", Title: "models", ScheduleMode: ScheduleOnce, RunAt: "2026-01-02 03:05:00", TriggerMode: TriggerLLM,
		Message: testElyphTask("models"), ToolListNames: []string{"shell"}, Enabled: true, Actor: actor, SourcePlatform: "cli"}
	for _, pair := range []config.ModelSelection{{Provider: "provider"}, {Model: "model"}, {Provider: "missing", Model: "model"}} {
		req.ModelProvider, req.Model = pair.Provider, pair.Model
		if _, err := svc.Create(ctx, req); err == nil {
			t.Fatalf("accepted invalid pair %+v", pair)
		}
	}
	req.ModelProvider, req.Model = "provider", "explicit"
	job, err := svc.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if meta := mustDecodeTestMetadata(t, job.Metadata); meta.LLM.Model != "explicit" || meta.LLM.ModelProvider != "provider" {
		t.Fatalf("metadata=%+v", meta.LLM)
	}
	job, err = svc.Update(ctx, PatchRequest{Name: req.Name, Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	if meta := mustDecodeTestMetadata(t, job.Metadata); meta.LLM.Model != "explicit" {
		t.Fatal("omitted update cleared model")
	}
	blank := ""
	if _, err := svc.Update(ctx, PatchRequest{Name: req.Name, Actor: actor, Model: &blank}); err == nil {
		t.Fatal("accepted partial clear")
	}
	job, err = svc.Update(ctx, PatchRequest{Name: req.Name, Actor: actor, Model: &blank, ModelProvider: &blank})
	if err != nil {
		t.Fatal(err)
	}
	if meta := mustDecodeTestMetadata(t, job.Metadata); meta.LLM.Model != "" || meta.LLM.ModelProvider != "" {
		t.Fatalf("clear failed: %+v", meta.LLM)
	}
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
		svc := NewService(Options{Store: fakeCronStore{cron: newFakeCronRepo()}, Models: models, Runner: runner})
		meta := Metadata{LLM: CronLLMMetadata{ToolListNames: []string{"shell"}}}
		want := "initial"
		if explicit {
			meta.LLM.ModelProvider, meta.LLM.Model = "provider", "explicit"
			want = "explicit"
		}
		if _, _, err := svc.runLLMReport(context.Background(), storage.CronJob{Name: "models"}, meta, CronDeliveryState{}); err != nil {
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
