package control

import (
	"context"
	"elbot/internal/contextinfo"
	"encoding/json"
	"errors"
	"testing"

	"elbot/internal/session/binding"
	"elbot/internal/session/share"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

func TestBackgroundPromotionCommitAndFailure(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := context.Background(), privateScope()
	old := f.create(t, scope, "old")
	bgScope := scope
	bgScope.ChatType, bgScope.ChatID = "", ""
	bgScope.TaskType, bgScope.TaskID = "cron", "task"
	row, err := f.s.PrepareBackground(ctx, bgScope, BackgroundRequest{TaskType: "cron", TaskID: "task", Metadata: map[string]string{"custom": "value"}})
	if err != nil {
		t.Fatal(err)
	}
	if row.Mode != storage.SessionModeBackground || f.s.CurrentBinding(scope).SessionID() != old.ID {
		t.Fatal("background preparation changed foreground")
	}
	fields, err := storage.DecodeSessionMetadata(row.Metadata)
	if err != nil || row.TaskType != "cron" || row.TaskID != "task" || string(fields["custom"]) != `"value"` {
		t.Fatalf("background metadata: %s %v", row.Metadata, err)
	}
	if _, err := f.s.PrepareBackground(ctx, bgScope, BackgroundRequest{SessionID: row.ID, TaskType: "cron"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PrepareBackground(ctx, scope, BackgroundRequest{SessionID: old.ID}); err == nil {
		t.Fatal("foreground silently converted into background")
	}
	failure := errors.New("incompatible protocol")
	f.s.deps.CheckForeground = func(context.Context, *storage.Session) error { return failure }
	if _, err := f.s.Resume(ctx, scope, row.ID); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	stored, err := f.store.Sessions().Get(ctx, row.ID)
	if err != nil || !share.IsBackground(stored) || share.WasPromoted(stored) {
		t.Fatalf("failed promotion committed: %+v %v", stored, err)
	}
	if f.s.CurrentBinding(scope).SessionID() != old.ID {
		t.Fatal("failed promotion changed current")
	}
	f.s.deps.CheckForeground = func(context.Context, *storage.Session) error { return nil }
	calls := 0
	f.s.deps.AdoptForeground = func(ctx context.Context, row *storage.Session, b *share.Binding) {
		calls++
		if ctx.Err() != nil || row.Mode != storage.SessionModeWork || row.ChatID != scope.ChatID || b != f.s.CurrentBinding(scope) {
			t.Error("incomplete committed adoption")
		}
	}
	promoted, err := f.s.Resume(ctx, scope, row.ID)
	if err != nil || promoted == nil || !share.WasPromoted(promoted) || calls != 1 {
		t.Fatalf("promotion: %+v %v calls=%d", promoted, err, calls)
	}
	if _, err := f.s.Resume(ctx, scope, row.ID); err != nil || calls != 1 {
		t.Fatalf("repeated promotion: %v calls=%d", err, calls)
	}
	if taken, err := f.s.SessionTakenOver(ctx, row.ID); err != nil || !taken {
		t.Fatalf("takeover fact: %v %v", taken, err)
	}
	if _, err := f.s.PrepareBackground(ctx, bgScope, BackgroundRequest{SessionID: row.ID}); !errors.Is(err, share.ErrForegroundSession) {
		t.Fatalf("reused promoted background: %v", err)
	}
}

func TestBindingNotificationFailureDoesNotPreventRelease(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := context.Background(), privateScope()
	row := f.create(t, scope, "old")
	b := f.s.CurrentBinding(scope)
	_, err := f.bindings.ObserveBindingChanged(func(context.Context, binding.BindingChangedEvent) error { return errors.New("notification failed") }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.ResetCurrent(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if b.Valid() || f.s.CurrentBinding(scope) != nil {
		t.Fatal("event failure undid unbinding")
	}
	if sessions, bindings := f.releaseCounts(); sessions != 1 || bindings != 1 {
		t.Fatalf("missed releases: %d %d", sessions, bindings)
	}
	if _, err := f.store.Sessions().Get(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
}

func TestReferencePromotesElwispTaskForSuperadmin(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	taskScope := share.Scope{Owner: contextinfo.ActorIdentity{ID: "watch:server"}, TaskType: "elnis", TaskID: "event:42"}
	row, err := f.s.PrepareBackground(t.Context(), taskScope, BackgroundRequest{TaskType: taskScope.TaskType, TaskID: taskScope.TaskID, Name: "Server health"})
	if err != nil {
		t.Fatal(err)
	}
	message := storage.Message{SessionID: row.ID, Role: storage.RoleAssistant}
	viewer := privateScope()
	resume, _, _, err := f.s.ResolveReference(t.Context(), viewer, message)
	if err != nil || resume != "" {
		t.Fatalf("user continuation: %q %v", resume, err)
	}
	if _, err := f.s.Resume(t.Context(), viewer, row.ID); err == nil {
		t.Fatal("unrelated user took over task")
	}
	viewer.Owner.ID = "admin"
	resume, _, _, err = f.s.ResolveReference(t.Context(), viewer, message)
	if err != nil || resume != row.ID {
		t.Fatalf("admin continuation: %q %v", resume, err)
	}
	promoted, err := f.s.Resume(t.Context(), viewer, resume)
	if err != nil {
		t.Fatal(err)
	}
	if promoted.Owner != viewer.Owner || promoted.Platform != viewer.Platform || promoted.ChatType != viewer.ChatType || promoted.ChatID != viewer.ChatID || promoted.TaskType != "" || promoted.TaskID != "" || promoted.TaskName != "" {
		t.Fatalf("promotion facts: %+v", promoted)
	}
	fields, err := storage.DecodeSessionMetadata(promoted.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	var origin ForegroundOrigin
	if err := json.Unmarshal(fields["foreground_origin"], &origin); err != nil {
		t.Fatal(err)
	}
	if origin.TaskType != taskScope.TaskType || origin.TaskID != taskScope.TaskID || origin.Owner != taskScope.Owner || origin.Name != "Server health" {
		t.Fatalf("lost task origin: %+v", origin)
	}
}
