// 验证媒体清理与运行时关闭的错误和日志语义。

package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/events"
	"elbot/internal/storage"
)

type releaseMediaAPI struct {
	mediaAPI
	release func(context.Context, storage.MediaReference) error
}

func (a releaseMediaAPI) RemoveReference(ctx context.Context, ref storage.MediaReference) error {
	return a.release(ctx, ref)
}

func TestMediaPruneKeepsIndependentFailureAndCancellation(t *testing.T) {
	bridge := newMediaBridge(releaseMediaAPI{release: func(_ context.Context, ref storage.MediaReference) error {
		if ref.MediaID == "cancel" {
			return context.Canceled
		}
		return errors.New("independent reference failure")
	}})
	for _, id := range []string{"cancel", "failure"} {
		bridge.leases[id] = &mediaLease{reference: storage.MediaReference{MediaID: id}}
	}
	outcome, err := bridge.prune(t.Context(), time.Now(), true)
	if err == nil || outcome.ResultStatus != events.ResultFailed || !strings.Contains(outcome.Content, "cancel") || !strings.Contains(outcome.Content, "independent reference failure") || len(bridge.leases) != 2 {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}

func TestMediaPruneKeepsDirectoryFailureAfterReferenceRelease(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	bridge := newMediaBridge(releaseMediaAPI{release: func(context.Context, storage.MediaReference) error { return nil }})
	bridge.leases["lease"] = &mediaLease{dir: filepath.Join(file, "child")}
	outcome, err := bridge.prune(t.Context(), time.Now(), true)
	if err == nil || outcome.ResultStatus != events.ResultFailed || !strings.Contains(outcome.Content, "directory_release_error") {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}

func TestRuntimeCloseUsesCallerMediaCleanupContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), "cleanup-budget", "caller"))
	cancel()
	observed := make(chan context.Context, 1)
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir(), Media: releaseMediaAPI{release: func(ctx context.Context, _ storage.MediaReference) error { observed <- ctx; return ctx.Err() }}})
	if err != nil {
		t.Fatal(err)
	}
	manager.media.leases["lease"] = &mediaLease{}
	records := captureHookRecords(t)
	manager.Close(ctx)
	select {
	case <-manager.closeDone:
	case <-time.After(time.Second):
		t.Fatal("close did not finish")
	}
	cleanupCtx := <-observed
	if cleanupCtx.Err() != context.Canceled || cleanupCtx.Value("cleanup-budget") != "caller" {
		t.Fatal("cleanup context replaced")
	}
	if len(*records) != 1 || (*records)[0].ResultStatus != events.ResultCanceled || !strings.Contains((*records)[0].Content, "reference_release_error") {
		t.Fatal(*records)
	}
}
