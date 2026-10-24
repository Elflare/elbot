// 管理插件进程启动、重启、状态转换和实际退出。

package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	globalevents "elbot/internal/events"
	hookoutput "elbot/internal/hook/output"
	hookprotocol "elbot/internal/hook/protocol"
	"elbot/internal/hook/share"
	"elbot/internal/llm"
	"elbot/internal/processenv"
)

type worker struct {
	manager *Manager
	config  Config

	mu           sync.Mutex
	status       Status
	detail       string
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	pending      map[string]chan frame
	starting     bool
	stopping     bool
	manualStop   bool
	restartDelay time.Duration
	done         chan struct{}
	active       int
	reload       func() (globalevents.LogResult, error)
	reloadArmed  bool
	reloadPrep   bool
	writeMu      sync.Mutex
	routeMu      sync.Mutex
	routeLocks   map[routeKey]chan struct{}
}

func newWorker(manager *Manager, config Config) *worker {
	return &worker{manager: manager, config: config, status: StatusStopped, restartDelay: time.Duration(config.Restart.InitialDelaySeconds) * time.Second, pending: map[string]chan frame{}, routeLocks: map[routeKey]chan struct{}{}}
}

func (w *worker) start() error {
	w.mu.Lock()
	w.manualStop = false
	w.mu.Unlock()
	return w.manager.startWorker(w)
}

func (w *worker) waitReady(ctx context.Context) (globalevents.LogResult, error) {
	timeout := time.Duration(w.config.StartupTimeoutSeconds) * time.Second
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		w.mu.Lock()
		status, detail := w.status, w.detail
		w.mu.Unlock()
		switch status {
		case StatusReady, StatusRunning:
			return globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "worker ready"}, nil
		case StatusFailed, StatusDegraded:
			if detail == "" {
				detail = string(status)
			}
			return globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("hook worker %q failed to start: %s", w.config.ID, detail)}, fmt.Errorf("hook worker %q failed to start: %s", w.config.ID, detail)
		}
		select {
		case <-ctx.Done():
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: ctx.Err().Error()}
			if ctx.Err() == context.Canceled {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
			w.mu.Lock()
			status, detail := w.status, w.detail
			w.mu.Unlock()
			if status == StatusFailed || status == StatusDegraded {
				logResult.ResultStatus = globalevents.ResultFailed
				logResult.Content += "\nworker_fault=" + detail
			}
			return logResult, ctx.Err()
		case <-deadline.C:
			return globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("hook worker %q startup timed out", w.config.ID)}, fmt.Errorf("hook worker %q startup timed out", w.config.ID)
		case <-ticker.C:
		}
	}
}

func (w *worker) info() Info {
	w.mu.Lock()
	defer w.mu.Unlock()
	return Info{ID: w.config.ID, Description: w.config.Description, Mode: w.config.ModeOrOnce(), Status: w.status, Detail: w.detail, Active: w.active}
}

func (w *worker) setStatus(status Status, detail string) {
	w.mu.Lock()
	w.status = status
	w.detail = strings.TrimSpace(detail)
	w.mu.Unlock()
}

func (w *worker) run() {
	w.mu.Lock()
	if w.cmd != nil || w.starting || w.stopping || w.manualStop {
		w.mu.Unlock()
		return
	}
	w.starting = true
	w.status = StatusStarting
	w.detail = ""
	w.done = make(chan struct{})
	w.mu.Unlock()

	argv := w.config.Command
	cwd, err := resolveCwd(w.config.Dir, w.config.Cwd)
	if err != nil {
		w.startFailed(err)
		return
	}
	processEnv := w.config.ProcessEnv
	cmd := processEnv.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	stdin, err := cmd.StdinPipe()
	if err != nil {
		w.startFailed(err)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		w.startFailed(err)
		return
	}
	cmd.Stderr = stderrLogger{hookID: w.config.ID, Log: w.manager.opts.Log}
	w.manager.mu.RLock()
	w.mu.Lock()
	if w.manager.closed || w.cmd != nil || w.stopping || w.manualStop {
		w.starting = false
		w.mu.Unlock()
		w.manager.mu.RUnlock()
		w.setStatus(StatusStopped, "")
		return
	}
	if err := cmd.Start(); err != nil {
		w.mu.Unlock()
		w.manager.mu.RUnlock()
		w.startFailed(err)
		return
	}
	w.cmd = cmd
	w.stdin = stdin
	w.starting = false
	w.pending = map[string]chan frame{}
	w.mu.Unlock()
	// Both loops belong to this admitted startup, even if Close begins next.
	w.manager.workWG.Add(2)
	w.manager.mu.RUnlock()
	go func() {
		defer w.manager.workWG.Done()
		w.readLoop(stdout)
	}()
	go func() {
		defer w.manager.workWG.Done()
		w.waitLoop(cmd)
	}()

	ctx, cancel := context.WithTimeout(w.manager.rootCtx, time.Duration(w.config.StartupTimeoutSeconds)*time.Second)
	defer cancel()
	schemas, err := w.schemas()
	if err != nil {
		w.startFailed(err)
		w.kill()
		return
	}
	init := systemInit{Version: protocolVersion, Hook: systemHook{ID: w.config.ID, Description: w.config.Description, Dir: w.config.Dir, Cwd: cwd, SharedDir: w.manager.opts.SharedDir}, Tools: schemas}
	if _, _, err := w.request(ctx, "system.init", init); err != nil {
		w.startFailed(err)
		w.kill()
		return
	}
	w.mu.Lock()
	w.restartDelay = time.Duration(w.config.Restart.InitialDelaySeconds) * time.Second
	w.mu.Unlock()
	w.setStatus(StatusReady, "")
}

func (w *worker) startFailed(err error) {
	w.mu.Lock()
	w.starting = false
	w.status = StatusFailed
	w.detail = strings.TrimSpace(err.Error())
	cmd := w.cmd
	w.mu.Unlock()
	w.manager.opts.Log(w.manager.rootCtx, globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelError, EventName: "hook_worker_start_failed", Module: "hook", Type: globalevents.ContentText, ContentLimit: 4096, Content: fmt.Sprintf("hook=%s startup_error=%v", w.config.ID, err), ResultStatus: globalevents.ResultFailed})
	_ = w.manager.opts.Notify(context.WithoutCancel(w.manager.rootCtx), globalevents.Notification{Text: fmt.Sprintf("Hook %s 启动失败：%v", w.config.ID, err), Level: slog.LevelWarn})
	if cmd == nil && w.shouldRestart() {
		w.restartLater()
	}
}

func (w *worker) waitLoop(cmd *exec.Cmd) {
	err := cmd.Wait()
	w.mu.Lock()
	stopping := w.stopping
	manualStop := w.manualStop
	// Publish exit facts under the same lock before waking pending requests.
	if !stopping && !manualStop {
		if w.status != StatusFailed && w.status != StatusDegraded {
			w.status = StatusDegraded
			w.detail = "hook process exited"
			if err != nil {
				w.detail = strings.TrimSpace(err.Error())
			}
		}
	} else {
		w.stopping = false
		w.status = StatusStopped
		w.detail = ""
	}
	w.cmd = nil
	w.stdin = nil
	for id, pending := range w.pending {
		delete(w.pending, id)
		close(pending)
	}
	close(w.done)
	w.mu.Unlock()
	if !stopping && !manualStop {
		w.manager.mu.Lock()
		w.manager.clearRoutesLocked(w.config.ID)
		for key, transient := range w.manager.transient {
			if transient == w {
				delete(w.manager.transient, key)
			}
		}
		w.manager.mu.Unlock()
		if w.shouldRestart() {
			w.restartLater()
		}
		return
	}
}

func (w *worker) shouldRestart() bool {
	return w.config.ModeOrOnce() == ModePersistent && (w.config.Restart.Strategy == "on_failure" || w.config.Restart.Strategy == "always")
}

func (w *worker) restartLater() {
	w.manager.mu.Lock()
	if w.manager.closed {
		w.manager.mu.Unlock()
		return
	}
	w.manager.workWG.Add(1)
	w.manager.mu.Unlock()
	go func() {
		defer w.manager.workWG.Done()
		w.mu.Lock()
		delay := w.restartDelay
		maxDelay := time.Duration(w.config.Restart.MaxDelaySeconds) * time.Second
		next := maxDelay
		if delay < maxDelay-delay {
			next = delay * 2
		}
		w.restartDelay = next
		w.mu.Unlock()
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-w.manager.rootCtx.Done():
			return
		}
		_ = w.manager.startWorker(w)
	}()
}

func (w *worker) stop(ctx context.Context) (logResult globalevents.LogResult, err error) {
	defer func() {
		if logResult.ResultStatus == globalevents.ResultFailed {
			w.manager.opts.Log(ctx, globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelError, EventName: "hook_worker_stop_failed", Module: "hook", Type: globalevents.ContentText, ContentLimit: 4096, Content: fmt.Sprintf("hook=%s %s", w.config.ID, logResult.Content), ResultStatus: logResult.ResultStatus})
			_ = w.manager.opts.Notify(context.WithoutCancel(ctx), globalevents.Notification{Text: fmt.Sprintf("Hook %s 停止失败：%v", w.config.ID, err), Level: slog.LevelWarn})
		}
	}()
	w.mu.Lock()
	if w.stopping {
		done := w.done
		w.mu.Unlock()
		if done == nil {
			return globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "worker stopped"}, nil
		}
		select {
		case <-done:
			return globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "worker stopped"}, nil
		case <-ctx.Done():
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: ctx.Err().Error()}
			if ctx.Err() == context.Canceled {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
			return logResult, ctx.Err()
		}
	}
	w.stopping = true
	w.manualStop = true
	cmd := w.cmd
	done := w.done
	w.mu.Unlock()
	if cmd == nil {
		w.mu.Lock()
		w.stopping = false
		w.status = StatusStopped
		w.detail = ""
		w.mu.Unlock()
		return globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "worker was not running"}, nil
	}
	w.setStatus(StatusStopping, "")
	shutdownCtx, cancel := context.WithTimeout(ctx, time.Duration(w.config.ShutdownTimeoutSeconds)*time.Second)
	_, logResult, err = w.request(shutdownCtx, "system.shutdown", map[string]any{})
	cancel()
	graceful := true
	timer := time.NewTimer(time.Duration(w.config.ShutdownTimeoutSeconds) * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-ctx.Done():
		w.kill()
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: ctx.Err().Error()}
		if ctx.Err() == context.Canceled {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return logResult, errors.Join(err, ctx.Err())
	case <-timer.C:
		graceful = false
		w.kill()
		killTimer := time.NewTimer(time.Duration(w.config.ShutdownTimeoutSeconds) * time.Second)
		defer killTimer.Stop()
		select {
		case <-done:
		case <-ctx.Done():
			return globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "graceful shutdown timed out; " + ctx.Err().Error()}, errors.Join(err, ctx.Err())
		case <-killTimer.C:
			return globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("hook worker %q did not exit after kill", w.config.ID)}, errors.Join(err, fmt.Errorf("hook worker %q did not exit after kill", w.config.ID))
		}
	}
	w.mu.Lock()
	w.stopping = false
	w.mu.Unlock()
	if !graceful {
		return globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("hook worker %q graceful shutdown timed out", w.config.ID)}, errors.Join(err, fmt.Errorf("hook worker %q graceful shutdown timed out", w.config.ID))
	}
	if err != nil {
		return logResult, fmt.Errorf("hook worker %q shutdown: %w", w.config.ID, err)
	}
	return globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "worker stopped"}, nil
}
func (w *worker) kill() {
	w.mu.Lock()
	cmd := w.cmd
	w.mu.Unlock()
	if cmd != nil {
		_ = processenv.KillProcessTree(cmd)
	}
}

func (w *worker) handle(ctx context.Context, event share.Event, continuation bool, defaults share.Control) (share.Event, globalevents.LogResult, error) {
	key := routeKeyFor(event)
	unlock, err := w.lockRoute(ctx, key)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if err == context.Canceled {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return event, logResult, err
	}
	defer unlock()
	w.mu.Lock()
	status := w.status
	if w.manualStop {
		w.mu.Unlock()
		err := fmt.Errorf("hook worker %q is stopping or stopped", w.config.ID)
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultRejected, Content: err.Error()}, err
	}
	if status == StatusReady || status == StatusRunning {
		w.active++
	}
	w.mu.Unlock()
	if status != StatusReady && status != StatusRunning {
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("hook worker %q is %s", w.config.ID, status)}, fmt.Errorf("hook worker %q is %s", w.config.ID, status)
	}
	defer w.finishHandle()
	w.setStatus(StatusRunning, "")
	defer func() {
		w.mu.Lock()
		if w.cmd != nil && w.status == StatusRunning {
			w.status = StatusReady
		}
		w.mu.Unlock()
	}()
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(w.config.EventTimeoutSeconds)*time.Second)
	w.manager.beginInvocation(key, w.config.ID, cancel)
	defer w.manager.endInvocation(key, cancel)
	defer cancel()
	token := w.manager.putToolContext(w.config.ID, requestCtx, event, time.Duration(w.config.EventTimeoutSeconds)*time.Second)
	params := eventHandle{
		EventHandleParams: hookprotocol.EventHandleParams{Event: event, Match: share.EventMatchContext(event)},
		Continuation:      continuation,
		ToolContext:       token,
	}
	response, logResult, err := w.request(requestCtx, "event.handle", params)
	if errors.Is(err, context.Canceled) {
		w.notifyCancel(event.Platform.ConversationID)
	}
	if err != nil {
		return event, logResult, err
	}
	var result eventResult
	if len(response) > 0 {
		if err := hookoutput.DecodeJSON(response, &result); err != nil {
			return event, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "decode hook event response: " + err.Error()}, fmt.Errorf("decode hook event response: %w", err)
		}
	}
	switch result.Status {
	case "", "completed":
	case "waiting":
		if err := w.manager.setLease(w.config.ID, event, result, defaults); err != nil {
			return event, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
		}
	default:
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "unsupported hook event status: " + result.Status}, fmt.Errorf("unsupported hook event status %q", result.Status)
	}
	outputs, err := hookoutput.BuildGroup(result.Group, hookoutput.BuildOptions{BaseDir: w.config.Dir})
	if err != nil {
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	event.Outputs = append(event.Outputs, outputs...)
	if result.Message != nil && result.Message.Segments != nil {
		segments, err := hookoutput.BuildMessageSegments(*result.Message.Segments, w.config.Dir)
		if err != nil {
			return event, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
		}
		event.Message.Segments = segments
		if event.Point == share.PointToolCallCompleted {
			event.Tool.Result = llm.SegmentsTextOnly(segments)
		}
	} else if result.Message != nil && result.Message.Text != nil {
		event.Message.Segments = llm.SetSegmentText(event.Message.Segments, *result.Message.Text)
		if event.Point == share.PointToolCallCompleted {
			event.Tool.Result = llm.SegmentsTextOnly(event.Message.Segments)
		}
	}
	consume := defaults.Consume
	stopPropagation := defaults.StopPropagation
	if result.PassThrough != nil {
		consume = !*result.PassThrough
		stopPropagation = !*result.PassThrough
	}
	if consume {
		event.Control.Consume = true
	}
	if stopPropagation {
		event.Control.StopPropagation = true
	}
	if continuation && result.Status != "waiting" {
		w.manager.clearLease(w.config.ID, event)
	}
	return event, logResult, nil
}

func (w *worker) finishHandle() {
	w.mu.Lock()
	w.active--
	reload := w.takeReloadLocked()
	w.mu.Unlock()
	w.runReload(reload)
}

func (w *worker) prepareSelfReload() (any, error) {
	w.mu.Lock()
	if w.status != StatusReady && w.status != StatusRunning {
		status := w.status
		w.mu.Unlock()
		return nil, fmt.Errorf("stateful hook %q cannot reload while %s", w.config.ID, status)
	}
	if w.reload != nil || w.reloadPrep {
		w.mu.Unlock()
		return nil, fmt.Errorf("stateful hook %q already has a reload scheduled", w.config.ID)
	}
	w.reloadPrep = true
	w.mu.Unlock()

	commit, err := w.manager.preparePluginReload(w.config.ID)
	w.mu.Lock()
	w.reloadPrep = false
	if err == nil {
		w.reload = commit
	}
	w.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return map[string]any{"scheduled": true}, nil
}

func (w *worker) armReload() {
	w.mu.Lock()
	w.reloadArmed = true
	reload := w.takeReloadLocked()
	w.mu.Unlock()
	w.runReload(reload)
}

func (w *worker) discardReload() {
	w.mu.Lock()
	w.reload = nil
	w.reloadArmed = false
	w.mu.Unlock()
}

func (w *worker) takeReloadLocked() func() (globalevents.LogResult, error) {
	if !w.reloadArmed || w.active > 0 || w.reload == nil {
		return nil
	}
	reload := w.reload
	w.reload = nil
	w.reloadArmed = false
	return reload
}

func (w *worker) runReload(reload func() (globalevents.LogResult, error)) {
	if reload == nil {
		return
	}
	w.manager.mu.Lock()
	if w.manager.closed {
		w.manager.mu.Unlock()
		return
	}
	w.manager.workWG.Add(1)
	w.manager.mu.Unlock()
	go func() {
		defer w.manager.workWG.Done()
		if logResult, _ := reload(); logResult.ResultStatus == globalevents.ResultFailed || logResult.ResultStatus == globalevents.ResultCanceled {
			level := slog.LevelInfo
			if logResult.ResultStatus == globalevents.ResultFailed {
				level = slog.LevelError
			}
			w.manager.opts.Log(context.Background(), globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        level,
				EventName:    "hook_plugin_reload_failed",
				Module:       "hook",
				Type:         globalevents.ContentText,
				ContentLimit: 4096,
				Content:      fmt.Sprintf("hook plugin reload: hook=%s\n%s", w.config.ID, logResult.Content),
				ResultStatus: logResult.ResultStatus,
			})
		}
	}()
}

func (w *worker) lockRoute(ctx context.Context, key routeKey) (func(), error) {
	w.routeMu.Lock()
	lock := w.routeLocks[key]
	if lock == nil {
		lock = make(chan struct{}, 1)
		lock <- struct{}{}
		w.routeLocks[key] = lock
	}
	w.routeMu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-lock:
		return func() { lock <- struct{}{} }, nil
	}
}

func (w *worker) notifyCancel(conversationID string) {
	params := map[string]string{}
	if strings.TrimSpace(conversationID) != "" {
		params["conversation_id"] = conversationID
	}
	_ = w.write(frame{Type: "event", Method: "event.cancel", Params: mustJSON(params)})
}

func (w *worker) request(ctx context.Context, method string, params any) (json.RawMessage, globalevents.LogResult, error) {
	id := randomID("host")
	response := make(chan frame, 1)
	w.mu.Lock()
	if w.stdin == nil {
		w.mu.Unlock()
		return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "hook process is not running"}, fmt.Errorf("hook process is not running")
	}
	w.pending[id] = response
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.pending, id)
		w.mu.Unlock()
	}()
	request, err := hookprotocol.NewRequest(id, method, params)
	if err != nil {
		return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	if err := w.write(request); err != nil {
		return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	select {
	case <-ctx.Done():
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: method + ": " + ctx.Err().Error()}
		if ctx.Err() == context.Canceled {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		w.mu.Lock()
		status, detail := w.status, w.detail
		w.mu.Unlock()
		if status == StatusFailed || status == StatusDegraded {
			logResult.ResultStatus = globalevents.ResultFailed
			logResult.Content += "\nworker_fault=" + detail
		}
		return nil, logResult, ctx.Err()
	case value, ok := <-response:
		if !ok {
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "hook process exited while waiting for " + method}
			w.mu.Lock()
			status, detail := w.status, w.detail
			w.mu.Unlock()
			if status == StatusFailed || status == StatusDegraded {
				logResult.Content += "\nworker_fault=" + detail
			}
			if ctx.Err() != nil {
				logResult.Content += "\ncontext_error=" + ctx.Err().Error()
			}
			return nil, logResult, fmt.Errorf("hook process exited while waiting for %s", method)
		}
		if value.OK == nil || !*value.OK {
			if value.Error == "" {
				value.Error = "hook request failed"
			}
			return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: value.Error}, errors.New(value.Error)
		}
		return value.Result, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: method + " completed"}, nil
	}
}

func (w *worker) write(value frame) error {
	data, err := hookprotocol.EncodeFrame(value)
	if err != nil {
		return err
	}
	if len(data)+1 > share.MaxProtocolFrameBytes {
		return fmt.Errorf("hook protocol stdin frame exceeds 16 MiB limit")
	}
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	w.mu.Lock()
	stdin := w.stdin
	w.mu.Unlock()
	if stdin == nil {
		return fmt.Errorf("hook process stdin is closed")
	}
	if _, err := fmt.Fprintf(stdin, "%s\n", data); err != nil {
		return fmt.Errorf("write hook protocol frame: %w", err)
	}
	return nil
}

func (w *worker) readLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), share.MaxProtocolFrameBytes)
	for scanner.Scan() {
		line := bytesTrim(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		value, err := hookprotocol.DecodeFrame(line)
		if err != nil {
			w.setStatus(StatusDegraded, "invalid hook.v2 stdout frame: "+err.Error())
			w.kill()
			return
		}
		switch value.Type {
		case "response":
			w.deliverResponse(value)
		case "request":
			if hookprotocol.ValidateID(value.ID, "plugin:") != nil || strings.TrimSpace(value.Method) == "" {
				w.setStatus(StatusDegraded, "invalid hook request frame")
				w.kill()
				return
			}
			w.manager.mu.Lock()
			if w.manager.closed {
				w.manager.mu.Unlock()
				ok := false
				_ = w.write(frame{Type: "response", ID: value.ID, OK: &ok, Error: ErrClosed.Error()})
				continue
			}
			w.manager.workWG.Add(1)
			w.manager.mu.Unlock()
			go func() {
				defer w.manager.workWG.Done()
				w.handlePluginRequest(value)
			}()
		case "event":
			w.handlePluginEvent(value)
		default:
			w.setStatus(StatusDegraded, "unsupported hook.v2 frame type "+value.Type)
			w.kill()
			return
		}
	}
	if err := scanner.Err(); err != nil {
		w.setStatus(StatusDegraded, "read hook stdout: "+err.Error())
	}
}

func bytesTrim(value []byte) []byte {
	return []byte(strings.TrimSpace(string(value)))
}

func (w *worker) deliverResponse(value frame) {
	if hookprotocol.ValidateID(value.ID, "host:") != nil {
		w.setStatus(StatusDegraded, "response id must use host: prefix")
		w.kill()
		return
	}
	w.mu.Lock()
	pending := w.pending[value.ID]
	w.mu.Unlock()
	if pending != nil {
		pending <- value
	}
}

func (w *worker) handlePluginEvent(value frame) {
	if value.Method == "hook.log" {
		w.manager.opts.Log(context.Background(), globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelInfo,
			EventName:    "stateful_hook_event",
			Module:       "hook",
			Type:         globalevents.ContentText,
			ResultStatus: globalevents.ResultUnspecified,
			Content:      fmt.Sprintf("stateful hook event: hook=%s\n%s", w.config.ID, value.Params),
			ContentLimit: 4096,
		})
	}
}

func (w *worker) handlePluginRequest(value frame) {
	result, err := w.pluginRequest(value)
	ok := err == nil
	response := frame{Type: "response", ID: value.ID, OK: &ok}
	if err != nil {
		response.Error = err.Error()
	} else {
		response.Result = mustJSON(result)
	}
	writeErr := w.write(response)
	if writeErr != nil {
		w.manager.opts.Log(context.Background(), globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelError,
			EventName:    "write_hook_request_response_failed",
			Module:       "hook",
			Type:         globalevents.ContentText,
			ContentLimit: 4096,
			Content:      fmt.Sprintf("write hook request response failed: hook=%s method=%s error=%v", w.config.ID, value.Method, writeErr),
			ResultStatus: globalevents.ResultFailed,
		})
	}
	if value.Method == "hooks.reload" && err == nil {
		if writeErr == nil {
			w.armReload()
		} else {
			w.discardReload()
		}
	}
}
