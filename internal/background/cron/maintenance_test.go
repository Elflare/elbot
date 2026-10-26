package cron

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"elbot/internal/background/maintenance"
	"elbot/internal/background/share"
	"elbot/internal/config"
	"elbot/internal/events"
	"elbot/internal/storage"
)

func TestMaintenanceHandlerPreservesContextAndResult(t *testing.T) {
	m := NewManager(newFakeCronRepo())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	boom := errors.New("cleanup failed")
	want := events.LogResult{ResultStatus: events.ResultFailed, Content: "deleted=2"}
	err := m.RegisterMaintenanceHandler("cleanup", func(got context.Context) (events.LogResult, error) {
		if got != ctx {
			t.Fatal("context was replaced")
		}
		return want, boom
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.handlers["cleanup"](ctx, storage.CronJob{})
	if !reflect.DeepEqual(got, want) || err != boom {
		t.Fatalf("result=%#v err=%v", got, err)
	}
}

func TestConfigureMaintenanceJob(t *testing.T) {
	repo := newFakeCronRepo()
	m := NewManager(repo)
	ctx := context.Background()
	req := share.MaintenanceJob{Name: "cleanup", Handler: "cleanup.handler", Schedule: "0 1 * * *", Enabled: true}
	if err := m.ConfigureMaintenanceJob(ctx, req); err != nil {
		t.Fatal(err)
	}
	job := repo.jobs[req.Name]
	if job.Name != req.Name || job.Handler != req.Handler || job.Schedule != req.Schedule || !job.Enabled || job.NextRunAt == nil {
		t.Fatalf("job=%#v", job)
	}
	req.Enabled = false
	if err := m.ConfigureMaintenanceJob(ctx, req); err != nil {
		t.Fatal(err)
	}
	if job.Enabled || job.NextRunAt != nil {
		t.Fatalf("disabled job=%#v", job)
	}
	if err := m.ConfigureMaintenanceJob(ctx, share.MaintenanceJob{Name: "missing"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := repo.jobs["missing"]; ok {
		t.Fatal("disabled missing job was created")
	}
	for _, invalid := range []share.MaintenanceJob{
		{Name: "invalid", Handler: "cleanup.handler", Schedule: "bad", Enabled: true},
		{Name: "", Handler: "cleanup.handler", Schedule: req.Schedule, Enabled: true},
		{Name: "invalid", Schedule: req.Schedule, Enabled: true},
	} {
		if err := m.ConfigureMaintenanceJob(ctx, invalid); err == nil {
			t.Fatalf("accepted invalid request: %#v", invalid)
		}
	}
	if m.started || m.scheduler != nil {
		t.Fatal("configuration started scheduler")
	}
}

type failingMaintenanceRepo struct {
	*fakeCronRepo
	err error
}

func (r failingMaintenanceRepo) Upsert(context.Context, storage.UpsertCronJobRequest) (*storage.CronJob, error) {
	return nil, r.err
}
func (r failingMaintenanceRepo) DisableByName(context.Context, string) error { return r.err }

func TestMaintenanceConfigurationPropagatesRepositoryErrors(t *testing.T) {
	boom := errors.New("storage failed")
	m := NewManager(failingMaintenanceRepo{newFakeCronRepo(), boom})
	for _, enabled := range []bool{true, false} {
		err := m.ConfigureMaintenanceJob(context.Background(), share.MaintenanceJob{Name: "cleanup", Handler: "cleanup.handler", Schedule: "0 1 * * *", Enabled: enabled})
		if err != boom {
			t.Fatalf("enabled=%v err=%v", enabled, err)
		}
	}
}

func TestSetupMaintenanceDoesNotStartManager(t *testing.T) {
	repo := newFakeCronRepo()
	m := NewManager(repo)
	cfg := &config.Config{Maintenance: config.MaintenanceConfig{SandboxCleanup: config.MaintenanceCleanupConfig{Schedule: "0 3 * * *"}}}
	if err := maintenance.SetupCron(context.Background(), m.ConfigureMaintenanceJob, cfg); err != nil {
		t.Fatal(err)
	}
	if m.started || m.scheduler != nil {
		t.Fatal("maintenance setup started scheduler")
	}
	if len(repo.jobs) != 1 || !repo.jobs["system.maintenance.media_cleanup"].Enabled {
		t.Fatalf("jobs=%#v", repo.jobs)
	}
}
