package elnis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"elbot/internal/config"
)

type Runtime struct {
	server *http.Server
}

func NewRuntime(cfg config.ElnisHTTPConfig, service *Service) *Runtime {
	mux := http.NewServeMux()
	r := &Runtime{}
	maxBodyBytes := cfg.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = 1024 * 1024
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/elvena/v1/events", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, Response{Accepted: false, Status: StatusFailed, Error: "method not allowed"})
			return
		}
		defer req.Body.Close()
		var event Request
		decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes))
		if err := decoder.Decode(&event); err != nil {
			writeJSON(w, http.StatusBadRequest, Response{Accepted: false, Status: StatusFailed, Error: fmt.Sprintf("invalid json: %v", err)})
			return
		}
		resp, err := service.Handle(req.Context(), bearerToken(req), event)
		status := http.StatusAccepted
		if err != nil && !resp.Accepted {
			status = http.StatusBadRequest
			if resp.Error == "unauthorized" {
				status = http.StatusUnauthorized
			}
		}
		writeJSON(w, status, resp)
	})
	addr := strings.TrimSpace(cfg.Addr)
	if addr == "" {
		addr = "127.0.0.1:32170"
	}
	r.server = &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return r
}

func (r *Runtime) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := r.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.server.Shutdown(shutdownCtx)
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

func bearerToken(req *http.Request) string {
	if value := strings.TrimSpace(req.Header.Get("Authorization")); value != "" {
		prefix := "Bearer "
		if strings.HasPrefix(value, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(value, prefix))
		}
	}
	return strings.TrimSpace(req.Header.Get("X-Elnis-Token"))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
