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
	"elbot/internal/media"
)

type releaseMediaAPI struct {
	mediaAPI
	release func(context.Context, media.Reference) error
}

func (a releaseMediaAPI) RemoveReference(ctx context.Context, ref media.Reference) error {
	return a.release(ctx, ref)
}

func TestMediaPruneKeepsIndependentFailureAndCancellation(t *testing.T) {
	bridge := newMediaBridge(releaseMediaAPI{release: func(_ context.Context, ref media.Reference) error {
		if ref.MediaID == "cancel" {
			return context.Canceled
		}
		return errors.New("independent reference failure")
	}})
	for _, id := range []string{"cancel", "failure"} {
		bridge.leases[id] = &mediaLease{retained: true, reference: media.Reference{MediaID: id}}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	outcomes, err := pruneMediaForTest(ctx, bridge, time.Now(), true)
	if err == nil || len(outcomes) != 2 || len(bridge.leases) != 2 {
		t.Fatalf("outcomes=%+v err=%v", outcomes, err)
	}
	for _, outcome := range outcomes {
		if strings.Contains(outcome.Content, "independent reference failure") {
			if outcome.ResultStatus != events.ResultFailed {
				t.Fatal(outcome)
			}
		} else if outcome.ResultStatus != events.ResultCanceled || !strings.Contains(outcome.Content, "context canceled") {
			t.Fatal(outcome)
		}
	}
}

func TestMediaPruneKeepsDirectoryFailureAfterReferenceRelease(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	bridge := newMediaBridge(releaseMediaAPI{release: func(context.Context, media.Reference) error { return nil }})
	bridge.leases["lease"] = &mediaLease{retained: true, dir: filepath.Join(file, "child")}
	outcomes, err := pruneMediaForTest(t.Context(), bridge, time.Now(), true)
	if err == nil || len(outcomes) != 1 || outcomes[0].ResultStatus != events.ResultFailed || !strings.Contains(outcomes[0].Content, "directory_release_error") {
		t.Fatalf("outcomes=%+v err=%v", outcomes, err)
	}
}

func TestRuntimeCloseUsesCallerMediaCleanupContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), "cleanup-budget", "caller"))
	cancel()
	observed := make(chan context.Context, 1)
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir(), Media: releaseMediaAPI{release: func(ctx context.Context, _ media.Reference) error { observed <- ctx; return ctx.Err() }}})
	if err != nil {
		t.Fatal(err)
	}
	manager.media.leases["lease"] = &mediaLease{retained: true}
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
