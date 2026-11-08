// remote_frontend.go 托管用户提供的静态前端，与远程 CLI 共用 HTTP 服务。
package remote

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"syscall"

	"elbot/internal/events"
)

func (s *RemoteServer) initFrontend(mux *http.ServeMux) {
	defer s.work.Done()
	frontend, err := newFrontendHandler(s.frontendDir)
	if err != nil {
		events.AsyncLog(s.workCtx, events.LogRecord{
			Category: events.LogCategoryLog, Level: slog.LevelError, EventName: "cli_frontend_init_failed",
			Module: "cli", Content: fmt.Sprintf("directory=%s error=%v", s.frontendDir, err),
			ResultStatus: events.ResultFailed,
		})
		return
	}
	if frontend == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closing {
		mux.Handle("/", frontend)
	}
}

func newFrontendHandler(frontendDir string) (http.Handler, error) {
	if frontendDir == "" {
		return nil, nil
	}
	root, err := os.OpenRoot(frontendDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open directory: %w", err)
	}
	defer root.Close()
	info, err := root.Stat("index.html")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat index.html: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("index.html is not a regular file")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		fail := func(err error) {
			if os.IsNotExist(err) {
				http.NotFound(w, req)
				return
			}
			events.AsyncLog(req.Context(), events.LogRecord{
				Category: events.LogCategoryLog, Level: slog.LevelError, EventName: "cli_frontend_request_failed",
				Module: "cli", Content: fmt.Sprintf("path=%s error=%v", req.URL.Path, err),
				ResultStatus: events.ResultFailed,
			})
			http.Error(w, "前端文件读取失败", http.StatusInternalServerError)
		}
		root, err := os.OpenRoot(frontendDir)
		if err != nil {
			fail(err)
			return
		}
		defer root.Close()
		name := strings.TrimPrefix(req.URL.Path, "/")
		if name == "" || strings.HasSuffix(name, "/") {
			name += "index.html"
		}
		// TODO: 需要 SPA 页面路由时，在此增加导航请求的 index.html 回退。
		// 非阻塞打开避免 FIFO 等特殊文件在类型检查前阻塞。
		file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			fail(err)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			fail(err)
			return
		}
		if !info.Mode().IsRegular() {
			http.NotFound(w, req)
			return
		}
		http.ServeContent(w, req, info.Name(), info.ModTime(), file)
	}), nil
}
