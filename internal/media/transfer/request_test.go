package transfer

import (
	"context"
	"elbot/internal/events"
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
	"elbot/internal/operationresult"
)

type fakeObjects struct {
	hold      func(context.Context, string) (operationresult.Result[share.HeldMedia], error)
	info      func(context.Context, string) (share.MediaInfo, error)
	url       func(context.Context, string, share.Input, share.ImportLimits) (share.MediaInfo, error)
	file      func(context.Context, string, share.Input, share.ImportLimits) (share.MediaInfo, error)
	bytes     func(context.Context, []byte, share.Input, share.ImportLimits) (share.MediaInfo, error)
	load      func(context.Context, share.HistoryKey) (map[int]share.HistoryEntry, error)
	associate func(context.Context, share.HistoryKey, int, string, string) error
}

func (f *fakeObjects) Hold(ctx context.Context, id string) (operationresult.Result[share.HeldMedia], error) {
	return f.hold(ctx, id)
}
func (f *fakeObjects) Info(ctx context.Context, id string) (operationresult.Result[share.MediaInfo], error) {
	item, err := f.info(ctx, id)
	return mediaResult(item, err)
}
func (f *fakeObjects) ImportURL(ctx context.Context, u string, input share.Input, limits share.ImportLimits) (operationresult.Result[share.MediaInfo], error) {
	item, err := f.url(ctx, u, input, limits)
	return mediaResult(item, err)
}
func (f *fakeObjects) ImportFile(ctx context.Context, path string, input share.Input, limits share.ImportLimits) (operationresult.Result[share.MediaInfo], error) {
	item, err := f.file(ctx, path, input, limits)
	return mediaResult(item, err)
}
func (f *fakeObjects) ImportBytes(ctx context.Context, b []byte, input share.Input, limits share.ImportLimits) (operationresult.Result[share.MediaInfo], error) {
	item, err := f.bytes(ctx, b, input, limits)
	return mediaResult(item, err)
}

func mediaResult[T any](data T, err error) (operationresult.Result[T], error) {
	if err != nil {
		return operationresult.Result[T]{}, err
	}
	return operationresult.Result[T]{Status: operationresult.Success, Data: data}, nil
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
			m := NewManager(&fakeObjects{hold: func(context.Context, string) (operationresult.Result[share.HeldMedia], error) {
				calls++
				return operationresult.Result[share.HeldMedia]{Status: operationresult.Success, Data: h}, nil
			}}, Options{Log: func(context.Context, events.LogRecord) {}, Backend: tc.mode, MaxDirectBase64Bytes: tc.limit})
			input := []llm.LLMMessage{{Segments: []llm.MessageSegment{{Type: llm.SegmentFile, MediaID: "one", Name: "https://secret/name?token=x"}, {Type: llm.SegmentImage, MediaID: "one"}}}}
			before := llm.CloneMessages(input)
			result, usageID, err := m.AcquireForLLM(t.Context(), input)
			out := result.Data
			if err != nil || calls != 1 || !reflect.DeepEqual(input, before) || tc.fail && (result.Status != operationresult.Rejected || usageID != "") || !tc.fail && (result.Status != operationresult.Success || usageID == "") {
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
			if !tc.fail {
				m.FinishUsages([]string{usageID})
				if err := m.CleanupUsages(t.Context(), nil); err != nil || h.closes.Load() != 1 {
					t.Fatalf("usageID repeats success: %v %d", err, h.closes.Load())
				}
			}
		})
	}
}

func TestAcquireMissingHoldBecomesPlaceholder(t *testing.T) {
	m := NewManager(&fakeObjects{hold: func(context.Context, string) (operationresult.Result[share.HeldMedia], error) {
		return operationresult.Result[share.HeldMedia]{Status: operationresult.NotFound, Message: "media unavailable"}, nil
	}}, Options{Log: func(context.Context, events.LogRecord) {}, Backend: "base64"})
	result, usageID, err := m.AcquireForLLM(t.Context(), []llm.LLMMessage{{Segments: []llm.MessageSegment{{Type: llm.SegmentFile, MediaID: "one", Name: "https://host/absent.txt?secret=x"}}}})
	if err != nil || result.Status != operationresult.Success || usageID != "" {
		t.Fatalf("result=%+v usage=%q err=%v", result, usageID, err)
	}
	segment := result.Data[0].Segments[0]
	if segment.Type != llm.SegmentText || strings.Contains(segment.Text, "secret") {
		t.Fatalf("missing media leaked source: %+v", segment)
	}
}

func TestAcquireStorageFailuresRemainErrors(t *testing.T) {
	fault, closeErr := errors.New("storage failed"), errors.New("reader close failed")
	for _, stage := range []string{"open", "sign", "read"} {
		t.Run(stage, func(t *testing.T) {
			h := &fakeHeld{info: share.MediaInfo{ID: "one", Name: "saved.txt", MIMEType: "text/plain", Size: 4}}
			var reader *failingReader
			if stage == "open" {
				h.open = func(context.Context) (io.ReadCloser, error) { return nil, fault }
			}
			if stage == "sign" {
				h.sign = func(context.Context) (string, error) { return "", fault }
			}
			if stage == "read" {
				reader = &failingReader{Reader: failingRead{fault}, closeErr: closeErr}
				h.open = func(context.Context) (io.ReadCloser, error) { return reader, nil }
			}
			m := NewManager(&fakeObjects{hold: func(context.Context, string) (operationresult.Result[share.HeldMedia], error) {
				return operationresult.Result[share.HeldMedia]{Status: operationresult.Success, Data: h}, nil
			}}, Options{Log: func(context.Context, events.LogRecord) {}, Backend: "base64", MaxDirectBase64Bytes: 100})
			if stage == "sign" {
				m.options.Backend = "s3"
			}
			result, usageID, err := m.AcquireForLLM(t.Context(), []llm.LLMMessage{{Segments: []llm.MessageSegment{{Type: llm.SegmentFile, MediaID: "one"}}}})
			if !errors.Is(err, fault) || result.Status == operationresult.Success || usageID != "" || h.closes.Load() != 1 {
				t.Fatalf("result=%+v usage=%q close=%d err=%v", result, usageID, h.closes.Load(), err)
			}
			if reader != nil && reader.closes != 1 {
				t.Fatal("reader not closed")
			}
		})
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
	logs := make(chan events.LogRecord, 8)
	m := NewManager(&fakeObjects{hold: func(_ context.Context, id string) (operationresult.Result[share.HeldMedia], error) {
		return operationresult.Result[share.HeldMedia]{Status: operationresult.Success, Data: byID[id]}, nil
	}}, Options{Log: func(_ context.Context, record events.LogRecord) { logs <- record }, Backend: "s3"})
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
		if !errors.Is(err, context.Canceled) || errors.Is(err, faultA) || errors.Is(err, faultB) {
			t.Fatalf("lost errors: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("workers did not exit")
	}
	wantLogs := map[string]bool{faultA.Error(): false, faultB.Error(): false}
	for range 2 {
		select {
		case record := <-logs:
			wantLogs[record.Content] = true
		case <-time.After(time.Second):
			t.Fatal("missing independent upload failure log")
		}
	}
	if !wantLogs[faultA.Error()] || !wantLogs[faultB.Error()] {
		t.Fatal(wantLogs)
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
	m := NewManager(&fakeObjects{hold: func(_ context.Context, id string) (operationresult.Result[share.HeldMedia], error) {
		if id == "first" {
			return operationresult.Result[share.HeldMedia]{Status: operationresult.Success, Data: first}, nil
		}
		return operationresult.Result[share.HeldMedia]{Status: operationresult.Success, Data: second}, nil
	}}, Options{Log: func(context.Context, events.LogRecord) {}, Backend: "s3"})
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
