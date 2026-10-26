package elnis

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"elbot/internal/config"
)

func NewRuntime(cfg config.ElnisHTTPConfig, service *Service) *Runtime {
	mux := http.NewServeMux()

	r := &Runtime{service: service, queue: make(chan QueuedLLMEvent, cfg.QueueSize), workers: cfg.Workers,
		failed: make(chan struct{}), done: make(chan struct{})}
	service.SetLLMEnqueuer(r.enqueueLLM)

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	handler := func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, Response{Accepted: false, Status: StatusFailed, Error: "method not allowed"})
			return
		}
		defer req.Body.Close()
		var event Request
		decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, cfg.MaxBodyBytes))
		if err := decodeSingleJSON(decoder, &event); err != nil {
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
	}
	mux.HandleFunc("/elvena/v2/events", handler)
	mux.HandleFunc("/elvena/v3/events", handler)
	r.server = &http.Server{
		Addr: cfg.Addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			r.mu.Lock()
			if !r.ready || r.closed {
				r.mu.Unlock()
				writeJSON(w, http.StatusServiceUnavailable, Response{Status: StatusFailed, Error: "elnis is not accepting events"})
				return
			}
			r.mu.Unlock()
			mux.ServeHTTP(w, req)
		}),
		BaseContext: func(net.Listener) context.Context { return r.ctx },
		ConnState: func(_ net.Conn, state http.ConnState) {
			switch state {
			case http.StateNew:
				r.connections.Add(1)
			case http.StateClosed:
				r.connections.Done()
			}
		},
		ReadHeaderTimeout: time.Duration(cfg.ReadHeaderTimeoutSeconds) * time.Second,
		ReadTimeout:       time.Duration(cfg.ReadTimeoutSeconds) * time.Second,
		WriteTimeout:      time.Duration(cfg.WriteTimeoutSeconds) * time.Second,
		IdleTimeout:       time.Duration(cfg.IdleTimeoutSeconds) * time.Second,
	}
	return r
}

func decodeSingleJSON(decoder *json.Decoder, target any) error {
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
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
