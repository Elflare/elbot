package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"elbot/internal/hook"
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
	stopping     bool
	manualStop   bool
	restartDelay time.Duration
	done         chan struct{}
	active       int
	reload       func() error
	reloadArmed  bool
	reloadPrep   bool
	writeMu      sync.Mutex
	routeMu      sync.Mutex
	routeLocks   map[routeKey]chan struct{}
}

func newWorker(manager *Manager, config Config) *worker {
	return &worker{manager: manager, config: config, status: StatusStopped, pending: map[string]chan frame{}, routeLocks: map[routeKey]chan struct{}{}}
}

func (w *worker) start() {
	w.mu.Lock()
	w.manualStop = false
	w.mu.Unlock()
	go w.run()
}

func (w *worker) info() Info {
	w.mu.Lock()
	defer w.mu.Unlock()
	return Info{ID: w.config.ID, Description: w.config.Description, Status: w.status, Detail: w.detail}
}

func (w *worker) setStatus(status Status, detail string) {
	w.mu.Lock()
	w.status = status
	w.detail = strings.TrimSpace(detail)
	w.mu.Unlock()
}

func (w *worker) run() {
	w.mu.Lock()
	if w.cmd != nil || w.stopping {
		w.mu.Unlock()
		return
	}
	w.status = StatusStarting
	w.detail = ""
	w.done = make(chan struct{})
	w.mu.Unlock()

	argv, err := splitCommand(w.config.Command)
	if err != nil {
		w.startFailed(err)
		return
	}
	cwd, err := resolveCwd(w.config.Dir, w.config.Cwd)
	if err != nil {
		w.startFailed(err)
		return
	}
	cmd := exec.Command(argv[0], argv[1:]...)
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
	cmd.Stderr = stderrLogger{logger: w.manager.opts.Logger, hookID: w.config.ID}
	if err := cmd.Start(); err != nil {
		w.startFailed(err)
		return
	}
	w.mu.Lock()
	w.cmd = cmd
	w.stdin = stdin
	w.pending = map[string]chan frame{}
	w.mu.Unlock()
	go w.readLoop(stdout)
	go w.waitLoop(cmd)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(w.config.StartupTimeoutSeconds)*time.Second)
	defer cancel()
	init := systemInit{Version: protocolVersion, Hook: systemHook{ID: w.config.ID, Description: w.config.Description, Dir: w.config.Dir, Cwd: cwd, SharedDir: w.manager.opts.SharedDir}, Tools: w.schemas()}
	if _, err := w.request(ctx, "system.init", init); err != nil {
		w.setStatus(StatusFailed, err.Error())
		w.kill()
		return
	}
	w.mu.Lock()
	w.restartDelay = time.Duration(w.config.Restart.InitialDelaySeconds) * time.Second
	w.mu.Unlock()
	w.setStatus(StatusReady, "")
}

func (w *worker) startFailed(err error) {
	w.setStatus(StatusFailed, err.Error())
	if w.shouldRestart() {
		go w.restartLater()
	}
}

func (w *worker) waitLoop(cmd *exec.Cmd) {
	err := cmd.Wait()
	w.mu.Lock()
	stopping := w.stopping
	w.cmd = nil
	w.stdin = nil
	for id, pending := range w.pending {
		delete(w.pending, id)
		close(pending)
	}
	if w.done != nil {
		close(w.done)
	}
	w.mu.Unlock()
	w.mu.Lock()
	manualStop := w.manualStop
	w.mu.Unlock()
	if !stopping && !manualStop {
		detail := "hook process exited"
		if err != nil {
			detail = err.Error()
		}
		w.setStatus(StatusDegraded, detail)
		w.manager.mu.Lock()
		w.manager.clearRoutesLocked(w.config.ID)
		w.manager.mu.Unlock()
		if w.shouldRestart() {
			go w.restartLater()
		}
		return
	}
	w.setStatus(StatusStopped, "")
}

func (w *worker) shouldRestart() bool {
	return w.config.Restart.Strategy == "on_failure" || w.config.Restart.Strategy == "always"
}

func (w *worker) restartLater() {
	w.mu.Lock()
	delay := w.restartDelay
	if delay <= 0 {
		delay = time.Duration(w.config.Restart.InitialDelaySeconds) * time.Second
	}
	next := delay * 2
	maxDelay := time.Duration(w.config.Restart.MaxDelaySeconds) * time.Second
	if next > maxDelay {
		next = maxDelay
	}
	w.restartDelay = next
	w.mu.Unlock()
	if delay <= 0 {
		return
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	<-timer.C
	w.mu.Lock()
	stopping := w.stopping || w.manualStop
	w.mu.Unlock()
	if !stopping {
		w.run()
	}
}

func (w *worker) stop(ctx context.Context) {
	w.mu.Lock()
	if w.stopping {
		w.mu.Unlock()
		return
	}
	w.stopping = true
	w.manualStop = true
	cmd := w.cmd
	done := w.done
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.stopping = false
		w.mu.Unlock()
	}()
	if cmd == nil {
		w.setStatus(StatusStopped, "")
		return
	}
	w.setStatus(StatusStopping, "")
	shutdownCtx, cancel := context.WithTimeout(ctx, time.Duration(w.config.ShutdownTimeoutSeconds)*time.Second)
	_, _ = w.request(shutdownCtx, "system.shutdown", map[string]any{})
	cancel()
	select {
	case <-done:
	case <-time.After(time.Duration(w.config.ShutdownTimeoutSeconds) * time.Second):
		w.kill()
	}
}

func (w *worker) kill() {
	w.mu.Lock()
	cmd := w.cmd
	w.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (w *worker) handle(ctx context.Context, event hook.Event, continuation bool, defaults hook.Control) (hook.Event, error) {
	key := routeKeyFor(event)
	unlock, err := w.lockRoute(ctx, key)
	if err != nil {
		return event, err
	}
	defer unlock()
	w.mu.Lock()
	status := w.status
	if status == StatusReady || status == StatusRunning {
		w.active++
	}
	w.mu.Unlock()
	if status != StatusReady && status != StatusRunning {
		return event, fmt.Errorf("stateful hook %q is %s", w.config.ID, status)
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
	params := eventHandle{Event: event, Match: eventMatch(event), Continuation: continuation, ToolContext: token}
	response, err := w.request(requestCtx, "event.handle", params)
	if err != nil {
		return event, err
	}
	var result eventResult
	if len(response) > 0 {
		if err := json.Unmarshal(response, &result); err != nil {
			return event, fmt.Errorf("decode hook event response: %w", err)
		}
	}
	switch result.Status {
	case "", "completed":
	case "waiting":
		if err := w.manager.setLease(w.config.ID, event, result, defaults); err != nil {
			return event, err
		}
	default:
		return event, fmt.Errorf("unsupported hook event status %q", result.Status)
	}
	outputs, err := w.outputs(result.Outputs)
	if err != nil {
		return event, err
	}
	event.Outputs = append(event.Outputs, outputs...)
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
	return event, nil
}

func (w *worker) finishHandle() {
	w.mu.Lock()
	if w.active > 0 {
		w.active--
	}
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
	if err == nil && commit == nil {
		err = fmt.Errorf("hook plugin reload returned an empty commit")
	}
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

func (w *worker) takeReloadLocked() func() error {
	if !w.reloadArmed || w.active > 0 || w.reload == nil {
		return nil
	}
	reload := w.reload
	w.reload = nil
	w.reloadArmed = false
	return reload
}

func (w *worker) runReload(reload func() error) {
	if reload == nil {
		return
	}
	go func() {
		if err := reload(); err != nil && w.manager.opts.Logger != nil {
			w.manager.opts.Logger.Warn("hook plugin reload failed", "hook", w.config.ID, "error", err)
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

func (w *worker) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := randomID("host")
	response := make(chan frame, 1)
	w.mu.Lock()
	if w.stdin == nil {
		w.mu.Unlock()
		return nil, fmt.Errorf("hook process is not running")
	}
	w.pending[id] = response
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.pending, id)
		w.mu.Unlock()
	}()
	if err := w.write(frame{Type: "request", ID: id, Method: method, Params: mustJSON(params)}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case value, ok := <-response:
		if !ok {
			return nil, fmt.Errorf("hook process exited while waiting for %s", method)
		}
		if value.OK == nil || !*value.OK {
			if value.Error == "" {
				value.Error = "hook request failed"
			}
			return nil, errors.New(value.Error)
		}
		return value.Result, nil
	}
}

func (w *worker) write(value frame) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
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
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytesTrim(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var value frame
		if err := json.Unmarshal(line, &value); err != nil {
			w.setStatus(StatusDegraded, "invalid hook.v2 stdout frame: "+err.Error())
			w.kill()
			return
		}
		switch value.Type {
		case "response":
			w.deliverResponse(value)
		case "request":
			if !strings.HasPrefix(value.ID, "plugin:") || strings.TrimSpace(value.Method) == "" {
				w.setStatus(StatusDegraded, "invalid hook request frame")
				w.kill()
				return
			}
			go w.handlePluginRequest(value)
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
	if !strings.HasPrefix(value.ID, "host:") {
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
	if value.Method == "hook.log" && w.manager.opts.Logger != nil {
		w.manager.opts.Logger.Info("stateful hook event", "hook", w.config.ID, "params", string(value.Params))
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
	if writeErr != nil && w.manager.opts.Logger != nil {
		w.manager.opts.Logger.Warn("write hook request response failed", "hook", w.config.ID, "method", value.Method, "error", writeErr)
	}
	if value.Method == "hooks.reload" && err == nil {
		if writeErr == nil {
			w.armReload()
		} else {
			w.discardReload()
		}
	}
}
