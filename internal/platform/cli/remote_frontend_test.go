package cli

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/events"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func testFrontendDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "frontend")
	for name, text := range map[string]string{
		"index.html":       "<h1>home</h1>",
		"settings.html":    "<h1>settings</h1>",
		"app.js":           "console.log('hello');",
		"assets/app.css":   "body { color: red; }",
		"pages/index.html": "<h1>pages</h1>",
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestFrontendFiles(t *testing.T) {
	handler, err := newFrontendHandler(testFrontendDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		method, path string
		status       int
		body, mime   string
	}{
		{http.MethodGet, "/", 200, "<h1>home</h1>", "text/html"},
		{http.MethodGet, "/index.html", 200, "<h1>home</h1>", "text/html"},
		{http.MethodGet, "/settings.html", 200, "<h1>settings</h1>", "text/html"},
		{http.MethodGet, "/pages/", 200, "<h1>pages</h1>", "text/html"},
		{http.MethodGet, "/app.js", 200, "console.log('hello');", "javascript"},
		{http.MethodGet, "/assets/app.css", 200, "body { color: red; }", "text/css"},
		{http.MethodHead, "/", 200, "", "text/html"},
		{http.MethodHead, "/assets/app.css", 200, "", "text/css"},
		{http.MethodGet, "/missing.js", 404, "", ""},
		{http.MethodGet, "/chat", 404, "", ""},
		{http.MethodGet, "/assets", 404, "", ""},
		{http.MethodGet, "/assets/", 404, "", ""},
		{http.MethodPost, "/", 405, "", ""},
	} {
		t.Run(tt.method+tt.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tt.method, tt.path, nil))
			if response.Code != tt.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tt.status, response.Body.String())
			}
			if tt.status == http.StatusOK {
				if response.Body.String() != tt.body || !strings.Contains(response.Header().Get("Content-Type"), tt.mime) {
					t.Fatalf("body=%q content-type=%q", response.Body.String(), response.Header().Get("Content-Type"))
				}
			}
			if tt.status == http.StatusMethodNotAllowed && response.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal("missing allowed methods")
			}
		})
	}
}

func TestFrontendDirectoryBoundary(t *testing.T) {
	dir := testFrontendDir(t)
	secret := filepath.Join(filepath.Dir(dir), ".env")
	if err := os.WriteFile(secret, []byte("SECRET"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"escape":      secret,
		"escape-dir":  filepath.Dir(dir),
		"app-link.js": "app.js",
	} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := newFrontendHandler(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/../.env", "/%2e%2e/.env", "/escape", "/escape-dir/.env"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code < 400 || strings.Contains(response.Body.String(), "SECRET") {
				t.Fatalf("escaped frontend: status=%d body=%q", response.Code, response.Body.String())
			}
		})
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/app-link.js", nil))
	if response.Code != http.StatusOK || response.Body.String() != "console.log('hello');" {
		t.Fatalf("internal symlink: status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestFrontendInitialization(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{"", filepath.Join(dir, "missing"), dir} {
		handler, err := newFrontendHandler(path)
		if err != nil || handler != nil {
			t.Fatalf("missing frontend %q: handler=%v error=%v", path, handler, err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "index.html"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := newFrontendHandler(dir); err == nil {
		t.Fatal("accepted non-file index.html")
	}
	if err := os.Remove(filepath.Join(dir, "index.html")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "index.html")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "index.html")); err != nil {
		t.Fatal(err)
	}
	if _, err := newFrontendHandler(dir); err == nil {
		t.Fatal("accepted index.html outside frontend")
	}
}

func TestFrontendReadsUpdatedFiles(t *testing.T) {
	dir := testFrontendDir(t)
	handler, err := newFrontendHandler(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"<h1>first</h1>", "<h1>updated</h1>"} {
		if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
		if response.Code != http.StatusOK || response.Body.String() != text {
			t.Fatalf("stale frontend: status=%d body=%q", response.Code, response.Body.String())
		}
	}
	if err := os.Remove(filepath.Join(dir, "index.html")); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("removed entry status=%d", response.Code)
	}
}

func TestRemoteFrontendSharesServerAndAuthentication(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "without frontend", true: "with frontend"}[enabled], func(t *testing.T) {
			dir := ""
			if enabled {
				dir = testFrontendDir(t)
			}
			s := testRemoteServerWithFrontend(t, dir)
			inputs := make(chan string, 1)
			if err := s.Start(t.Context(), func(_ context.Context, text string, _ share.MessageContext) error {
				inputs <- text
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			s.work.Wait()
			address := s.listener.Addr().String()
			client := &http.Client{Timeout: time.Second}
			response, err := client.Get("http://" + address + "/")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if enabled && (response.StatusCode != http.StatusOK || string(body) != "<h1>home</h1>") || !enabled && response.StatusCode != http.StatusNotFound {
				t.Fatalf("homepage: status=%d body=%q", response.StatusCode, body)
			}
			conn := dialTestRemote(t, s)
			if err := wsjson.Write(t.Context(), conn, remoteMessage{Type: remoteMsgInput, Text: "hello"}); err != nil {
				t.Fatal(err)
			}
			select {
			case text := <-inputs:
				if text != "hello" {
					t.Fatal(text)
				}
			case <-time.After(time.Second):
				t.Fatal("missing authenticated input")
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			unauthorized, _, err := websocket.Dial(ctx, "ws://"+address+defaultRemotePath, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer unauthorized.CloseNow()
			if err := wsjson.Write(ctx, unauthorized, remoteMessage{Type: remoteMsgHello, ClientID: "user", Token: "wrong"}); err != nil {
				t.Fatal(err)
			}
			if msg := readTestRemote(t, unauthorized); msg.Type != remoteMsgError || msg.Text != "unauthorized" {
				t.Fatalf("authentication bypass: %+v", msg)
			}

		})
	}
}

func TestFrontendRequestFailureIsLogged(t *testing.T) {
	dir := testFrontendDir(t)
	handler, err := newFrontendHandler(dir)
	if err != nil {
		t.Fatal(err)
	}
	logs := make(chan events.LogRecord, 1)
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
		if record.EventName == "cli_frontend_request_failed" {
			logs <- record
		}
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	if err := os.Rename(dir, dir+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusInternalServerError || response.Body.String() != "前端文件读取失败\n" {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	select {
	case record := <-logs:
		if record.Level != slog.LevelError || record.ResultStatus != events.ResultFailed || !strings.Contains(record.Content, dir) {
			t.Fatalf("lost failure details: %+v", record)
		}
	case <-time.After(time.Second):
		t.Fatal("missing frontend request failure log")
	}
}

func TestRemoteFrontendFailureDoesNotStopServer(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(dir, "index.html")
	if err := os.Mkdir(index, 0700); err != nil {
		t.Fatal(err)
	}
	logs := make(chan events.LogRecord, 1)
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
		if record.EventName == "cli_frontend_init_failed" && strings.Contains(record.Content, dir) {
			logs <- record
		}
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	// 非普通入口不再使构造失败；检查只在 Start 后执行。
	s := testRemoteServerWithFrontend(t, dir)
	select {
	case record := <-logs:
		t.Fatalf("checked frontend during construction: %+v", record)
	default:
	}
	if err := s.Start(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	dialTestRemote(t, s)
	s.work.Wait()
	select {
	case record := <-logs:
		if record.Level != slog.LevelError || record.ResultStatus != events.ResultFailed || !strings.Contains(record.Content, "not a regular file") {
			t.Fatalf("lost initialization error: %+v", record)
		}
	case <-time.After(time.Second):
		t.Fatal("missing frontend initialization failure log")
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index, []byte("fixed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusNotFound {
		t.Fatal("retried frontend initialization without restarting")
	}
}

func TestRemoteFrontendChecksAfterConstruction(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "frontend")
	s := testRemoteServerWithFrontend(t, dir)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("added before Start"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	s.work.Wait()
	response := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK || response.Body.String() != "added before Start" {
		t.Fatalf("frontend was checked before Start: status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestRemoteFrontendCloseWaitsAndPreventsRegistration(t *testing.T) {
	s := testRemoteServerWithFrontend(t, testFrontendDir(t))
	mux := http.NewServeMux()
	// 模拟已接纳、尚未调度的初始化任务。
	s.work.Add(1)
	s.BeginClose()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Wait(canceled); err != context.Canceled {
		t.Fatalf("reported exit while initialization is pending: %v", err)
	}
	s.initFrontend(mux)
	ctx, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	if err := s.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusNotFound {
		t.Fatal("registered frontend after closing")
	}
}
