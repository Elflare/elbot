package httpclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSSEFramesMultilineDataAndComments(t *testing.T) {
	client, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(": hello\r\nevent: example\r\nid: 3\r\ndata: {\"a\":\r\ndata: 1}\r\n\r\n: heartbeat\n\ndata: second\n\n"))}
	sse, err := client.OpenSSE(context.Background(), resp)
	if err != nil {
		t.Fatal(err)
	}
	defer sse.Close()
	first := <-sse.Frames
	second := <-sse.Frames
	end := <-sse.Frames
	if first.Event != "example" || string(first.Data) != "{\"a\":\n1}" || first.Err != nil {
		t.Fatalf("first=%+v", first)
	}
	if second.Event != "" || string(second.Data) != "second" || second.Err != nil {
		t.Fatalf("second=%+v", second)
	}
	if end.Err != io.EOF {
		t.Fatalf("end=%+v", end)
	}
}

func TestSSECancellationWakesBlockedConsumerAndClosesBody(t *testing.T) {
	closed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	client, _ := New(Options{})
	sse, err := client.OpenSSE(ctx, resp)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("body remained open")
	}
	select {
	case _, ok := <-sse.Frames:
		if ok {
			for range sse.Frames {
			}
		}
	case <-time.After(time.Second):
		t.Fatal("frame producer remained blocked")
	}
}
