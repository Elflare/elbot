package elnis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"elbot/internal/config"
)

type Runtime struct {
	server  *http.Server
	service *Service
	queue   chan QueuedLLMEvent
	workers int
}

const reportRecoveryInterval = 30 * time.Second

const (
	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 30 * time.Second
	defaultWriteTimeout      = 300 * time.Second
	defaultIdleTimeout       = 60 * time.Second
)

func NewRuntime(cfg config.ElnisHTTPConfig, service *Service) *Runtime {
	mux := http.NewServeMux()
	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = 128
	}
	workers := cfg.Workers
	if workers <= 0 {
		workers = 1
	}
	r := &Runtime{service: service, queue: make(chan QueuedLLMEvent, queueSize), workers: workers}
	service.SetLLMEnqueuer(r.enqueueLLM)
	maxBodyBytes := cfg.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = 1024 * 1024
	}
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
		decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes))
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
	addr := strings.TrimSpace(cfg.Addr)
	if addr == "" {
		addr = "127.0.0.1:32170"
	}
	r.server = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: configuredTimeout(cfg.ReadHeaderTimeoutSeconds, defaultReadHeaderTimeout),
		ReadTimeout:       configuredTimeout(cfg.ReadTimeoutSeconds, defaultReadTimeout),
		WriteTimeout:      configuredTimeout(cfg.WriteTimeoutSeconds, defaultWriteTimeout),
		IdleTimeout:       configuredTimeout(cfg.IdleTimeoutSeconds, defaultIdleTimeout),
	}
	return r
}

func configuredTimeout(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
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

func (r *Runtime) enqueueLLM(ctx context.Context, event QueuedLLMEvent) error {
	select {
	case r.queue <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return fmt.Errorf("elnis llm queue is full")
	}
}

func (r *Runtime) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if r.service != nil {
		if err := r.service.resetDeliveringReports(runCtx); err != nil {
			return fmt.Errorf("reset interrupted elnis reports: %w", err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < r.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.worker(runCtx)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.reportWorker(runCtx)
	}()
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
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = r.server.Shutdown(shutdownCtx)
		cancel()
		wg.Wait()
		return ctx.Err()
	case err := <-errCh:
		cancel()
		wg.Wait()
		return err
	}
}

func (r *Runtime) reportWorker(ctx context.Context) {
	if r.service == nil {
		return
	}
	if err := r.service.recoverReports(ctx, false); err != nil && ctx.Err() == nil {
		r.service.logWarn("initial elnis report recovery failed", "error", err.Error())
	}
	ticker := time.NewTicker(reportRecoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.service.recoverReports(ctx, false); err != nil && ctx.Err() == nil {
				r.service.logWarn("elnis report retry failed", "error", err.Error())
			}
		}
	}
}

func (r *Runtime) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-r.queue:
			if r.service != nil {
				_ = r.service.RunLLMEvent(ctx, event.Event, event.EventID)
			}
		}
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
