package hook

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"elbot/internal/events"
	"elbot/internal/hook/pipeline"
	"elbot/internal/hook/runtime"
	"elbot/internal/hook/share"
	hookoutput "elbot/internal/hook/share/output"
	"elbot/internal/testutil"
	"elbot/internal/testutil/mediatest"
)

func TestMediaImportKeepsPipelinePriorityAndResultDependency(t *testing.T) {
	center := mediatest.New(t, testutil.NewSQLiteStore(t), t.TempDir())
	log := func(context.Context, events.LogRecord) {}
	run, err := runtime.NewManager(runtime.Options{SharedDir: t.TempDir(), Media: center, Log: log, Notify: func(context.Context, events.Notification) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := run.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	started, unblock := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-unblock
		w.Header().Set("Content-Type", "image/png")
		_ = png.Encode(w, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	})
	p := pipeline.NewManager(log, nil)
	var order []string
	register := func(name string, priority int, download bool) {
		p.Register(share.Registration{Point: share.PointPlatformMessageReceived, Name: name, Priority: priority, Match: share.Always(), Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, events.LogResult, error) {
			if event.ID == "other" {
				return event, events.LogResult{}, nil
			}
			order = append(order, name)
			if download {
				segments, err := run.BuildMessageSegments(ctx, []hookoutput.MessageSegment{{Type: "image", URL: server.URL}}, "")
				event.Message.Segments = segments
				return event, events.LogResult{}, err
			}
			if len(event.Message.Segments) != 1 || event.Message.Segments[0].MediaID == "" {
				return event, events.LogResult{}, fmt.Errorf("%s ran without prior media", name)
			}
			return event, events.LogResult{}, nil
		})})
	}
	register("B", 20, false)
	register("A", 10, true)
	register("C", 20, false)
	done := make(chan error, 1)
	go func() {
		_, _, err := p.Run(t.Context(), share.Event{ID: "main", Point: share.PointPlatformMessageReceived}, pipeline.CallOptions{Woken: true})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first Hook did not start")
	}
	select {
	case err := <-done:
		t.Fatal("chain did not wait", err)
	default:
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, _, err := p.Run(ctx, share.Event{ID: "other", Point: share.PointPlatformMessageReceived}, pipeline.CallOptions{Woken: true}); err != nil {
		t.Fatal("unrelated chain blocked", err)
	}
	close(unblock)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("chain did not finish")
	}
	if !reflect.DeepEqual(order, []string{"A", "B", "C"}) {
		t.Fatal("priority or registration order changed", order)
	}
}
