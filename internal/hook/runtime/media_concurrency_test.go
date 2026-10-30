package runtime

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	hookoutput "elbot/internal/hook/share/output"
	"elbot/internal/media"
)

type mediaProbe struct {
	mediaAPI
	importURL func(context.Context, string, media.Input, media.ImportLimits) (media.MediaInfo, error)
	export    func(context.Context, string, string) (media.MediaInfo, error)
	remove    func(context.Context, media.Reference) error
}

func (p mediaProbe) ImportURL(ctx context.Context, url string, input media.Input, limits media.ImportLimits) (media.MediaInfo, error) {
	return p.importURL(ctx, url, input, limits)
}
func (p mediaProbe) Export(ctx context.Context, id, path string) (media.MediaInfo, error) {
	return p.export(ctx, id, path)
}
func (p mediaProbe) RemoveReference(ctx context.Context, ref media.Reference) error {
	if p.remove != nil {
		return p.remove(ctx, ref)
	}
	return p.mediaAPI.RemoveReference(ctx, ref)
}

func awaitMediaResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("media call blocked")
		return nil
	}
}

func TestSlowMediaImportDoesNotBlockTextAndCancellation(t *testing.T) {
	m, center, dir, _ := testMediaRuntime(t)
	started, unblock := make(chan struct{}), make(chan struct{})
	m.media.api = mediaProbe{mediaAPI: center, importURL: func(ctx context.Context, _ string, input media.Input, limits media.ImportLimits) (media.MediaInfo, error) {
		close(started)
		<-unblock
		return center.ImportBytes(ctx, []byte("image"), media.Input{MIMEType: "image/png"}, limits)
	}}
	t.Cleanup(func() {
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	})
	done := make(chan error, 1)
	go func() {
		_, err := m.BuildMessageSegments(t.Context(), []hookoutput.MessageSegment{{Type: "image", URL: "https://example.test/image"}}, dir)
		done <- err
	}()
	<-started
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		if cancelled {
			cancel()
		}
		textDone := make(chan error, 1)
		go func() {
			_, err := m.BuildMessageSegments(ctx, []hookoutput.MessageSegment{{Type: "text", Text: "ready"}}, dir)
			textDone <- err
		}()
		err := awaitMediaResult(t, textDone)
		cancel()
		if cancelled && !errors.Is(err, context.Canceled) || !cancelled && err != nil {
			t.Fatal(err)
		}
	}
	close(unblock)
	if err := awaitMediaResult(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseWaitCancellationPruneAndCloseDuringExport(t *testing.T) {
	m, center, dir, store := testMediaRuntime(t)
	item, err := center.ImportBytes(t.Context(), []byte("content"), media.Input{}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	started, unblock := make(chan struct{}), make(chan struct{})
	m.media.api = mediaProbe{mediaAPI: center, export: func(ctx context.Context, id, path string) (media.MediaInfo, error) {
		close(started)
		<-unblock
		// 模拟已进入不可中断 I/O；Close 必须等真实调用退出。
		return center.Export(context.WithoutCancel(ctx), id, path)
	}}
	t.Cleanup(func() {
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	})
	done := make(chan error, 1)
	go func() {
		_, err := m.MediaRequest(t.Context(), dir, "media.export", mustJSON(map[string]string{"media": item.ID}))
		done <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := m.MediaRequest(ctx, dir, "media.export", mustJSON(map[string]string{"media": item.ID})); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("same-lease wait ignored cancellation", err)
	}
	if _, err := pruneMediaForTest(t.Context(), m.media, time.Now().Add(2*mediaLeaseTTL), false); err != nil {
		t.Fatal(err)
	}
	refs, err := store.MediaReferences().ListByOwner(t.Context(), "hook", m.media.owner)
	if err != nil || len(refs) != 1 {
		t.Fatal("maintenance released active lease", refs, err)
	}
	closeCtx, closeCancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer closeCancel()
	if err := m.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("close did not wait", err)
	}
	select {
	case <-m.Done():
		t.Fatal("runtime finished before export")
	default:
	}
	close(unblock)
	if err := awaitMediaResult(t, done); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	refs, err = store.MediaReferences().ListByOwner(t.Context(), "hook", m.media.owner)
	if err != nil || len(refs) != 0 {
		t.Fatal("late lease leaked", refs, err)
	}
	if len(m.media.leases) != 0 {
		t.Fatal("export directory retained after close")
	}
}

func TestLeaseCleanupBlocksOnlySameLeaseAndReacquiresAfterRemoval(t *testing.T) {
	m, center, dir, _ := testMediaRuntime(t)
	a, err := center.ImportBytes(t.Context(), []byte("a"), media.Input{}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := center.ImportBytes(t.Context(), []byte("b"), media.Input{}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	first := mediaCall(t, m, dir, "media.export", map[string]string{"media": a.ID})
	started, unblock := make(chan struct{}), make(chan struct{})
	var removals atomic.Int32
	m.media.api = mediaProbe{mediaAPI: center, export: center.Export, remove: func(ctx context.Context, ref media.Reference) error {
		if removals.Add(1) == 1 {
			close(started)
			<-unblock
		}
		return center.RemoveReference(ctx, ref)
	}}
	t.Cleanup(func() {
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	})
	done := make(chan error, 1)
	go func() {
		_, err := pruneMediaForTest(t.Context(), m.media, time.Now().Add(2*mediaLeaseTTL), false)
		done <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := m.MediaRequest(ctx, dir, "media.export", mustJSON(map[string]string{"media": a.ID})); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	other := make(chan error, 1)
	go func() {
		_, err := m.MediaRequest(t.Context(), dir, "media.metadata", mustJSON(map[string]string{"media": b.ID}))
		other <- err
	}()
	if err := awaitMediaResult(t, other); err != nil {
		t.Fatal(err)
	}
	close(unblock)
	if err := awaitMediaResult(t, done); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired file survived", err)
	}
	second := mediaCall(t, m, dir, "media.export", map[string]string{"media": a.ID})
	if second.Path == first.Path {
		t.Fatal("used removed lease")
	}
	if data, err := os.ReadFile(second.Path); err != nil || string(data) != "a" {
		t.Fatal(string(data), err)
	}
}

func TestCloseRetryRetainsFailedLease(t *testing.T) {
	m, center, dir, _ := testMediaRuntime(t)
	item, err := center.ImportBytes(t.Context(), []byte("a"), media.Input{}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	mediaCall(t, m, dir, "media.metadata", map[string]string{"media": item.ID})
	var fail atomic.Bool
	fail.Store(true)
	var attempts atomic.Int32
	m.media.api = mediaProbe{mediaAPI: center, remove: func(ctx context.Context, ref media.Reference) error {
		attempts.Add(1)
		if fail.Load() {
			return errors.New("release unavailable")
		}
		return center.RemoveReference(ctx, ref)
	}}
	if err := m.Close(t.Context()); err == nil || attempts.Load() != 1 {
		t.Fatal("close retried or hid failure", err, attempts.Load())
	}
	fail.Store(false)
	if err := m.Close(t.Context()); err != nil || attempts.Load() != 2 || len(m.media.leases) != 0 {
		t.Fatal(err, attempts.Load())
	}
}
