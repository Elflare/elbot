package background

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/agent"
	elcron "elbot/internal/background/cron"
	"elbot/internal/background/share"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/modelmgr"
	"elbot/internal/security"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/testutil"
	"elbot/internal/tool"
)

func TestCronBridgeOpaqueRecordsErrorsAndDeliveryTransaction(t *testing.T) {
	store := testutil.NewSQLiteStore(t)
	b := cronBridge{repo: store.CronJobs()}
	ctx := t.Context()
	row, err := store.CronJobs().Upsert(ctx, storage.UpsertCronJobRequest{Name: "job", Handler: elcron.UserHandlerName, Schedule: "0 3 * * *", Enabled: true, Metadata: "{invalid metadata"})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := b.ListEnabled(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].Metadata != row.Metadata || jobs[0].ID != row.ID {
		t.Fatalf("opaque records=%#v,%v", jobs, err)
	}
	if _, err := b.GetByName(ctx, "missing"); !errors.Is(err, elcron.ErrJobNotFound) {
		t.Fatalf("missing=%v", err)
	}
	if err := b.DisableByName(ctx, "missing"); !errors.Is(err, elcron.ErrJobNotFound) {
		t.Fatalf("missing disable=%v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := b.GetByName(canceled, row.Name); !errors.Is(err, context.Canceled) {
		t.Fatalf("read error=%v", err)
	}
	if err := store.Sessions().Create(ctx, &storage.Session{ID: "session", OwnerID: "owner", Platform: "cli", PlatformScopeID: "cron:job", Mode: storage.SessionModeBackground, Status: storage.SessionStatusActive}); err != nil {
		t.Fatal(err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{ID: "message", SessionID: "session", Role: storage.RoleAssistant, Content: "report"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := b.CompareAndSwapDelivery(ctx, row.ID, "", "current", `{"delivery_complete":true}`, nil); err != nil || !ok {
		t.Fatal(ok, err)
	}
	mappings := []share.ReportMessageMap{{Platform: "telegram", PlatformScopeID: "private:1", PlatformMessageID: "sent", SessionID: "session", MessageID: "message"}}
	if ok, err := b.CompareAndSwapDelivery(ctx, row.ID, "stale", "obsolete", `{}`, mappings); err != nil || ok {
		t.Fatalf("stale commit=%v,%v", ok, err)
	}
	mapped, err := store.Messages().FindByPlatformMessage(ctx, "telegram", "private:1", "sent")
	if err != nil || mapped.ID != "message" {
		t.Fatalf("actual send mapping=%#v,%v", mapped, err)
	}
	got, err := b.GetByName(ctx, row.Name)
	if err != nil || got.DeliveryToken != "current" || got.DeliveryState != `{"delivery_complete":true}` {
		t.Fatalf("stale overwrite=%#v,%v", got, err)
	}
	if disabled, err := b.DisableByNameIfDeliveryToken(ctx, row.Name, "stale"); err != nil || disabled {
		t.Fatal(disabled, err)
	}
	if disabled, err := b.DisableByNameIfDeliveryToken(ctx, row.Name, "current"); err != nil || !disabled {
		t.Fatal(disabled, err)
	}
}

type cronBridgeModels struct{}

func (cronBridgeModels) ResolveMode(string) modelmgr.Selection {
	return modelmgr.Selection{ModelSelection: config.ModelSelection{Provider: "provider", Model: "model"}}
}
func (cronBridgeModels) ValidateSelection(config.ModelSelection) error { return nil }

type cronBridgeRunner struct{}

func (cronBridgeRunner) RunBackground(context.Context, share.RunRequest) (share.RunResult, error) {
	return share.RunResult{SessionID: "session", MessageID: "message", Text: `{"completed":true,"need_report":true,"report":"report"}`}, nil
}

func TestCronBridgeServicePersistsReportMappingAndCompletion(t *testing.T) {
	store := testutil.NewSQLiteStore(t)
	b := cronBridge{repo: store.CronJobs()}
	ctx := t.Context()
	if err := store.Sessions().Create(ctx, &storage.Session{ID: "session", OwnerID: "owner", Platform: "cli", PlatformScopeID: "cron:job", Mode: storage.SessionModeBackground, Status: storage.SessionStatusActive}); err != nil {
		t.Fatal(err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{ID: "message", SessionID: "session", Role: storage.RoleAssistant, Content: "report"}); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(storage.CronMetadata{Kind: storage.CronMetadataKind, Version: 2, Title: "report", Schedule: storage.CronSchedule{Mode: "once", RunAt: time.Now().Add(time.Hour).Format("2006-01-02 15:04:05")}, Trigger: storage.CronTrigger{Mode: "llm", Message: "#task report - test"}, Target: storage.CronTarget{AllEnabledPlatforms: true, SourcePlatform: "cli"}})
	if err != nil {
		t.Fatal(err)
	}
	row, err := store.CronJobs().Upsert(ctx, storage.UpsertCronJobRequest{Name: "user.cron.report", Handler: elcron.UserHandlerName, Schedule: "0 3 * * *", Enabled: true, Metadata: string(metadata)})
	if err != nil {
		t.Fatal(err)
	}
	m := elcron.NewManager(b)
	s := elcron.NewService(elcron.Options{Store: b, Manager: m, Models: cronBridgeModels{}, Runner: cronBridgeRunner{}, SandboxRoot: t.TempDir(),
		SessionTakenOver: func(context.Context, string) (bool, error) { return false, nil },
		CopyBackground:   func(context.Context, agent.BackgroundCopyRequest) (string, error) { return "copy", nil },
		EnabledPlatforms: []elcron.PlatformTarget{{Name: "telegram", SuperadminIDs: []string{"1"}}},
		SendTarget: func(_ context.Context, target delivery.Target, _ []delivery.Output) (delivery.Receipt, events.LogResult, error) {
			result := events.LogResult{ResultStatus: events.ResultSucceeded}
			if target.Platform == "cli" {
				return delivery.Receipt{}, result, nil
			}
			return delivery.Receipt{SentMessages: []delivery.SentMessage{{Platform: "telegram", ScopeID: "private:1", PlatformMessageID: "notice"}}}, result, nil
		},
	})
	t.Cleanup(func() { _ = s.Close(context.Background()); <-m.Stop().Done() })
	if _, err := s.Handler(ctx, *cronJob(row)); err != nil {
		t.Fatal(err)
	}
	message, err := store.Messages().FindByPlatformMessage(ctx, "telegram", "private:1", "notice")
	if err != nil || message.ID != "message" || message.SessionID != "session" {
		t.Fatalf("report mapping=%#v,%v", message, err)
	}
	stored, err := store.CronJobs().GetByName(ctx, row.Name)
	if err != nil {
		t.Fatal(err)
	}
	var status storage.CronTaskStatus
	if err := json.Unmarshal([]byte(stored.DeliveryState), &status); err != nil {
		t.Fatal(err)
	}
	if !status.DeliveryComplete || stored.Enabled {
		t.Fatalf("completion JSON incompatible: %#v %#v", status, stored)
	}
}

func TestToolNotificationRefreshesCronThroughRealStorage(t *testing.T) {
	store := testutil.NewSQLiteStore(t)
	cfg := config.Default()
	cfg.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
	cfg.Sandbox.Root = t.TempDir()
	tools, err := tool.NewSupervisor(t.Context(), cfg, tool.SupervisorDeps{Store: store, Permission: security.DefaultPermission()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tools.Close(context.Background()) })
	if err := tools.Start(); err != nil {
		t.Fatal(err)
	}
	b := cronBridge{repo: store.CronJobs()}
	m := elcron.NewManager(b)
	if err := m.RegisterHandler(elcron.UserHandlerName, func(context.Context, elcron.Job) (events.LogResult, error) {
		return events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	}); err != nil {
		t.Fatal(err)
	}
	connection, err := tools.ObserveCronChanged(func(event tool.CronChangedEvent) error { return m.RefreshJob(event.Name) }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Disconnect(); <-m.Stop().Done() })
	scheduled := make(chan events.LogRecord, 16)
	logs, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.EventName == "cron_job_scheduled" && strings.Contains(record.Content, "user.cron.chain") {
			scheduled <- record
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logs.Disconnect() })
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: contextinfo.RoleSuperadmin})
	write := func(raw string) {
		t.Helper()
		if _, _, err := tools.CallTool(ctx, tool.CallRequest{Name: "cron_write", Arguments: json.RawMessage(raw)}); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"operation":"create","name":"chain","schedule_mode":"cron","cron_expr":"0 3 * * *","trigger_mode":"direct","message":"hello"}`)
	select {
	case <-scheduled:
		t.Fatal("notification started scheduler before Start")
	default:
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// Startup loads the initial row and drains the cached definition notification.
	for {
		select {
		case <-scheduled:
			continue
		default:
			goto drained
		}
	}
drained:
	write(`{"operation":"update","name":"chain","cron_expr":"0 4 * * *"}`)
	select {
	case record := <-scheduled:
		if !strings.Contains(record.Content, "0 4 * * *") {
			t.Fatal(record.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("Tool update did not reach scheduler")
	}
	job, err := store.CronJobs().GetByName(ctx, "user.cron.chain")
	if err != nil || job.NextRunAt == nil || job.NextRunAt.Hour() != 4 {
		t.Fatalf("updated schedule=%#v,%v", job, err)
	}
	write(`{"operation":"disable","name":"chain"}`)
	job, err = store.CronJobs().GetByName(ctx, "user.cron.chain")
	if err != nil || job.Enabled || job.NextRunAt != nil {
		t.Fatalf("disabled=%#v,%v", job, err)
	}
}
