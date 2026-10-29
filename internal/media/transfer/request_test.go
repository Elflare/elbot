package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/llm"
	"elbot/internal/media/share"
)

type fakeObjects struct {
	hold      func(context.Context, string) (share.HeldMedia, error)
	info      func(context.Context, string) (share.MediaInfo, error)
	url       func(context.Context, string, share.Input, share.ImportLimits) (share.MediaInfo, error)
	file      func(context.Context, string, share.Input, share.ImportLimits) (share.MediaInfo, error)
	bytes     func(context.Context, []byte, share.Input, share.ImportLimits) (share.MediaInfo, error)
	load      func(context.Context, share.HistoryKey) (map[int]share.HistoryEntry, error)
	associate func(context.Context, share.HistoryKey, int, string, string) error
}

func (f *fakeObjects) Hold(ctx context.Context, id string) (share.HeldMedia, error) {
	return f.hold(ctx, id)
}
func (f *fakeObjects) Info(ctx context.Context, id string) (share.MediaInfo, error) {
	return f.info(ctx, id)
}
func (f *fakeObjects) ImportURL(ctx context.Context, u string, input share.Input, limits share.ImportLimits) (share.MediaInfo, error) {
	return f.url(ctx, u, input, limits)
}
func (f *fakeObjects) ImportFile(ctx context.Context, path string, input share.Input, limits share.ImportLimits) (share.MediaInfo, error) {
	return f.file(ctx, path, input, limits)
}
func (f *fakeObjects) ImportBytes(ctx context.Context, b []byte, input share.Input, limits share.ImportLimits) (share.MediaInfo, error) {
	return f.bytes(ctx, b, input, limits)
}
func (f *fakeObjects) LoadHistoryMedia(ctx context.Context, key share.HistoryKey) (map[int]share.HistoryEntry, error) {
	return f.load(ctx, key)
}
func (f *fakeObjects) AssociateHistory(ctx context.Context, key share.HistoryKey, index int, kind, id string) error {
	return f.associate(ctx, key, index, kind, id)
}

type fakeHeld struct {
	info                 share.MediaInfo
	open                 func(context.Context) (io.ReadCloser, error)
	sign                 func(context.Context) (string, error)
	close                func() error
	opens, signs, closes atomic.Int32
}

func (h *fakeHeld) Info() share.MediaInfo { return h.info }
func (h *fakeHeld) Open(ctx context.Context) (io.ReadCloser, error) {
	h.opens.Add(1)
	if h.open != nil {
		return h.open(ctx)
	}
	return io.NopCloser(strings.NewReader("body")), nil
}
func (h *fakeHeld) SignedURL(ctx context.Context) (string, error) {
	h.signs.Add(1)
	if h.sign != nil {
		return h.sign(ctx)
	}
	return "https://media.test/" + h.info.ID, nil
}
func (h *fakeHeld) Close(context.Context) error {
	h.closes.Add(1)
	if h.close != nil {
		return h.close()
	}
	return nil
}

type failingReader struct {
	io.Reader
	closeErr error
	closes   int
}

func (r *failingReader) Close() error { r.closes++; return r.closeErr }

type failingRead struct{ err error }

func (r failingRead) Read([]byte) (int, error) { return 0, r.err }
func missing() error                           { return &share.UnavailableError{Err: errors.New("missing object")} }

func TestAcquireModesDedupNamesAndIsolation(t *testing.T) {
	for _, tc := range []struct {
		mode         string
		limit        int64
		remote, fail bool
	}{
		{"base64", 8, false, false}, {"base64", 7, false, true}, {"hybrid", 8, false, false}, {"hybrid", 7, true, false}, {"s3", 8, true, false},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.mode, tc.limit), func(t *testing.T) {
			h := &fakeHeld{info: share.MediaInfo{ID: "one", Name: "literal%2520.txt", MIMEType: "text/plain", Size: 4}}
			calls := 0
			m := NewManager(&fakeObjects{hold: func(context.Context, string) (share.HeldMedia, error) { calls++; return h, nil }}, Options{Backend: tc.mode, MaxDirectBase64Bytes: tc.limit})
			input := []llm.LLMMessage{{Segments: []llm.MessageSegment{{Type: llm.SegmentFile, MediaID: "one", Name: "https://secret/name?token=x"}, {Type: llm.SegmentImage, MediaID: "one"}}}}
			before := llm.CloneMessages(input)
			out, usageID, err := m.AcquireForLLM(t.Context(), input)
			if (err != nil) != tc.fail || (!tc.fail && usageID == "") || calls != 1 || !reflect.DeepEqual(input, before) {
				t.Fatalf("out=%+v err=%v holds=%d", out, err, calls)
			}
			if !tc.fail {
				for _, s := range out[0].Segments {
					want := "data:text/plain;base64,Ym9keQ=="
					if tc.remote {
						want = "https://media.test/one"
					}
					if s.Name != h.info.Name || s.MIMEType != h.info.MIMEType || s.URL != want {
						t.Fatalf("segment=%+v", s)
					}
				}
				if h.closes.Load() != 0 {
					t.Fatal("released before consumer finishes")
				}
				if tc.remote && (h.signs.Load() != 1 || h.opens.Load() != 0) || !tc.remote && (h.opens.Load() != 1 || h.signs.Load() != 0) {
					t.Fatal("duplicated or wrong delivery")
				}
			}
			m.FinishUsages([]string{usageID})
			if err := m.CleanupUsages(t.Context(), nil); err != nil || h.closes.Load() != 1 {
				t.Fatalf("usageID repeats success: %v %d", err, h.closes.Load())
			}
		})
	}
}

func TestAcquireUnavailableAndIndependentFailures(t *testing.T) {
	fault := errors.New("close failed")
	for _, stage := range []string{"hold", "open", "sign", "read-close"} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", stage, mixed), func(t *testing.T) {
				problem := missing()
				if mixed {
					problem = errors.Join(problem, fault)
				}
				h := &fakeHeld{info: share.MediaInfo{ID: "one", Name: "saved%20.txt", MIMEType: "text/plain", Size: 4}}
				var reader *failingReader
				if stage == "open" {
					h.open = func(context.Context) (io.ReadCloser, error) { return nil, problem }
				}
				if stage == "sign" {
					h.sign = func(context.Context) (string, error) { return "", problem }
				}
				if stage == "read-close" {
					reader = &failingReader{Reader: failingRead{missing()}}
					if mixed {
						reader.closeErr = fault
					}
					h.open = func(context.Context) (io.ReadCloser, error) { return reader, nil }
				}
				m := NewManager(&fakeObjects{hold: func(context.Context, string) (share.HeldMedia, error) {
					if stage == "hold" {
						return nil, problem
					}
					return h, nil
				}}, Options{Backend: "base64", MaxDirectBase64Bytes: 100})
				if stage == "sign" {
					m.options.Backend = "s3"
				}
				out, usageID, err := m.AcquireForLLM(t.Context(), []llm.LLMMessage{{Segments: []llm.MessageSegment{{Type: llm.SegmentFile, MediaID: "one", Name: "https://host/absent.txt?secret=x"}}}})
				if mixed {
					if !errors.Is(err, fault) || out != nil {
						t.Fatalf("swallowed independent error: %v", err)
					}
				} else if err != nil || out[0].Segments[0].Type != llm.SegmentText || strings.Contains(out[0].Segments[0].Text, "secret") {
					t.Fatalf("missing result=%+v err=%v", out, err)
				}
				if reader != nil && reader.closes != 1 {
					t.Fatal("reader not closed")
				}
				m.FinishUsages([]string{usageID})
				if err := m.CleanupUsages(t.Context(), nil); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestRemoteCancellationWaitsAndPreservesFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{}, requestUploadConcurrency)
	finish := make(chan struct{})
	var active, peak atomic.Int32
	faultA, faultB := errors.New("sign A"), errors.New("sign B")
	holds := make([]*fakeHeld, requestUploadConcurrency+3)
	var messages []llm.MessageSegment
	for i := range holds {
		h := &fakeHeld{info: share.MediaInfo{ID: fmt.Sprint(i), Size: 1}}
		h.sign = func(ctx context.Context) (string, error) {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			started <- struct{}{}
			<-finish
			if i == 0 {
				return "", faultA
			}
			if i == 1 {
				return "", faultB
			}
			return "", ctx.Err()
		}
		h.close = func() error {
			if active.Load() != 0 {
				t.Error("released while worker is active")
			}
			return nil
		}
		holds[i] = h
		messages = append(messages, llm.MessageSegment{Type: llm.SegmentFile, MediaID: h.info.ID})
	}
	byID := map[string]*fakeHeld{}
	for _, h := range holds {
		byID[h.info.ID] = h
	}
	m := NewManager(&fakeObjects{hold: func(_ context.Context, id string) (share.HeldMedia, error) { return byID[id], nil }}, Options{Backend: "s3"})
	done := make(chan error, 1)
	go func() {
		_, _, err := m.AcquireForLLM(ctx, []llm.LLMMessage{{Segments: messages}})
		done <- err
	}()
	for range requestUploadConcurrency {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("returned before workers exit: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(finish)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, faultA) || !errors.Is(err, faultB) {
			t.Fatalf("lost errors: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("workers did not exit")
	}
	if peak.Load() != requestUploadConcurrency {
		t.Fatalf("peak=%d", peak.Load())
	}
	for i, h := range holds {
		if h.closes.Load() != 1 || i >= requestUploadConcurrency && h.signs.Load() != 0 {
			t.Fatalf("item %d signs=%d closes=%d", i, h.signs.Load(), h.closes.Load())
		}
	}
}

func TestRemoteFailureCancelsAndWaitsForPeers(t *testing.T) {
	fault := errors.New("upload failed")
	started := make(chan struct{})
	exited := make(chan struct{})
	first := &fakeHeld{info: share.MediaInfo{ID: "first"}}
	second := &fakeHeld{info: share.MediaInfo{ID: "second"}}
	first.sign = func(context.Context) (string, error) {
		<-started
		return "", fault
	}
	second.sign = func(ctx context.Context) (string, error) {
		close(started)
		<-ctx.Done()
		close(exited)
		return "", ctx.Err()
	}
	first.close = func() error {
		select {
		case <-exited:
		default:
			t.Error("released before canceled peer exited")
		}
		return nil
	}
	m := NewManager(&fakeObjects{hold: func(_ context.Context, id string) (share.HeldMedia, error) {
		if id == "first" {
			return first, nil
		}
		return second, nil
	}}, Options{Backend: "s3"})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, usageID, err := m.AcquireForLLM(ctx, []llm.LLMMessage{{Segments: []llm.MessageSegment{
		{Type: llm.SegmentFile, MediaID: "first"}, {Type: llm.SegmentFile, MediaID: "second"},
	}}})
	if !errors.Is(err, fault) || ctx.Err() != nil || first.closes.Load() != 1 || second.closes.Load() != 1 {
		t.Fatalf("peer not canceled by failure: %v parent=%v", err, ctx.Err())
	}
	m.FinishUsages([]string{usageID})
	if err := m.CleanupUsages(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
}
