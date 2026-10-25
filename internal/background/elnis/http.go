package elnis

import (
	"context"
	background "elbot/internal/background/share"
	"elbot/internal/contextinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"elbot/internal/config"
	globalevents "elbot/internal/events"
)

type Runtime struct {
	server  *http.Server
	service *Service
	queue   chan QueuedLLMEvent
	workers int
}

const reportRecoveryInterval = 30 * time.Second

func NewRuntime(cfg config.ElnisHTTPConfig, service *Service) *Runtime {
	mux := http.NewServeMux()

	r := &Runtime{service: service, queue: make(chan QueuedLLMEvent, cfg.QueueSize), workers: cfg.Workers}
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
		Addr:              cfg.Addr,
		Handler:           mux,
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
	if err := r.service.resetDeliveringReports(runCtx); err != nil {
		return fmt.Errorf("reset interrupted elnis reports: %w", err)
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
		shutdownErr := r.server.Shutdown(shutdownCtx)
		cancel()
		wg.Wait()
		if shutdownErr != nil {
			return errors.Join(ctx.Err(), shutdownErr)
		}
		return ctx.Err()
	case err := <-errCh:
		cancel()
		wg.Wait()
		return err
	}
}

func (r *Runtime) reportWorker(ctx context.Context) {
	identity := globalevents.LogIdentity{}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	if facts.SessionID != "" {
		identity.SessionID = &facts.SessionID
	}
	if facts.RunID != "" {
		identity.RunID = &facts.RunID
	}
	if facts.Attempt != "" {
		identity.Attempt = &facts.Attempt
	}
	if facts.RequestID != "" {
		identity.RequestID = &facts.RequestID
	}
	if facts.RootRequestID != "" {
		identity.RootRequestID = &facts.RootRequestID
	}

	if logResult, _ := r.service.recoverReports(ctx, false); logResult.ResultStatus == globalevents.ResultFailed || logResult.ResultStatus == globalevents.ResultCanceled {
		{
			content, _ := json.Marshal(map[string]any{"result": fmt.Sprintf("%.2048s", logResult.Content)})
			record := globalevents.LogRecord{
				Category:     globalevents.LogElnis,
				Level:        slog.LevelWarn,
				EventName:    "initial_elnis_report_recovery_failed",
				Module:       "elnis",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ResultStatus: logResult.ResultStatus,
			}
			if logResult.ResultStatus == globalevents.ResultCanceled {
				record.Level = slog.LevelInfo
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
		}
	}
	ticker := time.NewTicker(reportRecoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if logResult, _ := r.service.recoverReports(ctx, false); logResult.ResultStatus == globalevents.ResultFailed || logResult.ResultStatus == globalevents.ResultCanceled {
				{
					content, _ := json.Marshal(map[string]any{"result": fmt.Sprintf("%.2048s", logResult.Content)})
					record := globalevents.LogRecord{
						Category:     globalevents.LogElnis,
						Level:        slog.LevelWarn,
						EventName:    "elnis_report_retry_failed",
						Module:       "elnis",
						Type:         globalevents.ContentJSON,
						Content:      string(content),
						ResultStatus: logResult.ResultStatus,
					}
					if logResult.ResultStatus == globalevents.ResultCanceled {
						record.Level = slog.LevelInfo
					}
					record.Identity = identity
					background.LogBackground(ctx, record)
				}
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
			// RunLLMEvent records failures at their source.
			_ = r.service.RunLLMEvent(ctx, event.Event, event.EventID)
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
