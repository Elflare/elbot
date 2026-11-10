package local

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"
	"github.com/muesli/cancelreader"

	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	clishare "elbot/internal/platform/cli/share"
	platformshare "elbot/internal/platform/share"
	"elbot/internal/signal"
)

// Runtime owns local terminal input, rendering, and accepted callbacks.
type Runtime struct {
	name        string
	clientID    string
	input       *os.File
	interactive bool
	output      chan tea.Msg

	mu           sync.Mutex
	started      bool
	closing      bool
	closed       bool
	inputStopped bool
	startDone    chan struct{}
	inputDone    chan struct{}
	renderDone   chan struct{}
	done         chan struct{}
	startErr     error
	runErr       error
	closeErr     error
	workCtx      context.Context
	workCancel   context.CancelFunc
	renderCancel context.CancelFunc
	inputReader  cancelreader.CancelReader
	stopParent   func() bool
	work         sync.WaitGroup
	completer    func(context.Context, completion.Request) ([]completion.Item, error)
}

var _ clishare.LocalRuntime = (*Runtime)(nil)
var nextStreamID atomic.Uint64

func New(platformName, clientID string) *Runtime {
	if clientID == "" {
		clientID = "user"
	}
	return &Runtime{
		name: platformName, clientID: clientID, input: os.Stdin, interactive: isTerminal(os.Stdin),
		output: make(chan tea.Msg, 256), startDone: make(chan struct{}),
		inputDone: make(chan struct{}), renderDone: make(chan struct{}), done: make(chan struct{}),
	}
}

func (r *Runtime) Name() string { return r.name }

func (r *Runtime) SetCompleter(complete func(context.Context, completion.Request) ([]completion.Item, error)) {
	r.mu.Lock()
	r.completer = complete
	r.mu.Unlock()
}

func (r *Runtime) Start(ctx context.Context, handler platformshare.MessageHandler) (err error) {
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return signal.ErrClosed
	}
	if r.started {
		done := r.startDone
		r.mu.Unlock()
		select {
		case <-done:
			return r.startErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.started = true
	r.workCtx, r.workCancel = context.WithCancel(ctx)
	renderCtx, renderCancel := context.WithCancel(context.WithoutCancel(ctx))
	r.renderCancel = renderCancel
	r.stopParent = context.AfterFunc(ctx, r.BeginClose)
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.startErr = err
		close(r.startDone)
		r.mu.Unlock()
		if err != nil {
			r.BeginClose()
		}
	}()

	messageHandler := func(callCtx context.Context, text string) error {
		info := contextinfo.Conversation{
			PlatformData: r,
			Source:       contextinfo.Source{Platform: r.name, ChatType: contextinfo.ChatPrivate, ChatID: "local", Name: r.clientID},
			Identity:     contextinfo.Identity{ID: "local", Name: r.clientID},
		}
		msg := platformshare.MessageContext{CommandReply: r, Conversation: info, GroupRole: contextinfo.GroupRoleUnknown, RawText: text, Sender: r}
		return handler(contextinfo.WithConversation(callCtx, info), text, msg)
	}
	submit := func(text string) error {
		if !r.admit() {
			return signal.ErrClosed
		}
		defer r.work.Done()
		return messageHandler(r.workCtx, text)
	}

	if !isTerminal(r.input) {
		close(r.renderDone)
		info, statErr := r.input.Stat()
		if statErr != nil {
			r.finishInput(nil)
			return statErr
		}
		var reader io.Reader = r.input
		if !info.Mode().IsRegular() {
			cancellable, readErr := cancelreader.NewReader(r.input)
			if readErr != nil {
				r.finishInput(nil)
				return readErr
			}
			r.mu.Lock()
			r.inputReader = cancellable
			if r.closing {
				cancellable.Cancel()
			}
			r.mu.Unlock()
			reader = cancellable
		}
		go r.scan(reader, submit)
	} else {
		ready := make(chan struct{})
		go func() {
			complete := func(_ context.Context, req completion.Request) ([]completion.Item, error) {
				if !r.admit() {
					return nil, signal.ErrClosed
				}
				defer r.work.Done()
				r.mu.Lock()
				fn := r.completer
				r.mu.Unlock()
				if fn == nil {
					return nil, nil
				}
				return fn(r.workCtx, req)
			}
			runErr := runTUI(tuiModel{
				ctx: renderCtx, inputCtx: r.workCtx, handler: func(_ context.Context, text string) error { return submit(text) },
				completion: complete, output: r.output, userName: r.clientID, assistantName: "assistant",
				ready: func() { close(ready) }, stop: r.BeginClose,
			}, nil)
			if runErr != nil {
				r.mu.Lock()
				closing := r.closing
				r.mu.Unlock()
				if closing {
					runErr = signal.ErrClosed
				}
			}
			r.finishInput(runErr)
			close(r.renderDone)
		}()
		select {
		case <-ready:
		case <-r.renderDone:
			r.mu.Lock()
			err = r.runErr
			r.runErr = nil
			r.mu.Unlock()
			return err
		case <-r.workCtx.Done():
			r.mu.Lock()
			err = r.runErr
			r.runErr = nil
			r.mu.Unlock()
			if err != nil {
				return err
			}
			return r.workCtx.Err()
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func isTerminal(input *os.File) bool { return isatty.IsTerminal(input.Fd()) }

func (r *Runtime) scan(reader io.Reader, submit func(string) error) {
	scanner := bufio.NewScanner(reader)
	fmt.Print("> ")
	for scanner.Scan() {
		if r.workCtx.Err() != nil {
			break
		}
		text := scanner.Text()
		if text == "/exit" {
			break
		}
		_ = submit(text)
		fmt.Print("> ")
	}
	err := scanner.Err()
	if errors.Is(err, cancelreader.ErrCanceled) && r.workCtx.Err() != nil {
		err = nil
	}
	r.finishInput(err)
}

func (r *Runtime) admit() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return false
	}
	r.work.Add(1)
	return true
}

func (r *Runtime) finishInput(err error) {
	r.mu.Lock()
	r.runErr = err
	if !r.inputStopped {
		r.inputStopped = true
		close(r.inputDone)
	}
	if r.workCancel != nil {
		r.workCancel()
	}
	r.mu.Unlock()
	r.BeginClose()
}

func (r *Runtime) BeginClose() {
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return
	}
	r.closing = true
	started := r.started
	if r.workCancel != nil {
		r.workCancel()
	}
	if r.inputReader != nil {
		r.inputReader.Cancel()
	}
	if !started || isTerminal(r.input) {
		if !r.inputStopped {
			r.inputStopped = true
			close(r.inputDone)
		}
	}
	if !started {
		close(r.renderDone)
	}
	r.mu.Unlock()
	go func() {
		if started {
			<-r.startDone
		}
		<-r.inputDone
		r.work.Wait()
		close(r.done)
	}()
}

func (r *Runtime) WaitExit(ctx context.Context) error {
	select {
	case <-r.inputDone:
	case <-ctx.Done():
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runErr != nil {
		return r.runErr
	}
	if r.inputStopped {
		return nil
	}
	return ctx.Err()
}

func (r *Runtime) Wait(ctx context.Context) error {
	select {
	case <-r.done:
	case <-ctx.Done():
		select {
		case <-r.done:
		default:
			r.mu.Lock()
			err := r.runErr
			r.mu.Unlock()
			if err != nil {
				return err
			}
			return ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runErr
}

func (r *Runtime) Close(ctx context.Context) error {
	r.BeginClose()
	select {
	case <-r.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.mu.Lock()
	if r.renderCancel != nil {
		r.renderCancel()
	}
	r.mu.Unlock()
	select {
	case <-r.renderDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		if r.inputReader != nil {
			r.closeErr = r.inputReader.Close()
		}
		if r.stopParent != nil {
			r.stopParent()
		}
	}
	return r.closeErr
}

func (r *Runtime) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	if target.Platform != "" && target.Platform != r.name {
		return delivery.Receipt{}, fmt.Errorf("cli cannot send to platform %q", target.Platform)
	}
	text := delivery.FallbackOutput(outputs).Text
	if text == "" {
		return delivery.Receipt{}, nil
	}
	if !r.interactive {
		_, err := fmt.Fprintln(os.Stdout, strings.TrimSuffix(text, "\n"))
		return delivery.Receipt{}, err
	}
	return delivery.Receipt{}, r.enqueue(ctx, tuiOutputMsg(text))
}

func (r *Runtime) SendReply(ctx context.Context, outputs []delivery.Output) error {
	_, err := r.SendTo(ctx, delivery.Target{}, outputs)
	return err
}

func (r *Runtime) SendCommandResult(ctx context.Context, req command.Request, result *command.Result, failure *command.Error) (delivery.Receipt, error) {
	var outputs []delivery.Output
	if result != nil {
		outputs = append(outputs, delivery.Text(platformshare.FormatCommandResult(req, result)))
	}
	if failure != nil {
		outputs = append(outputs, delivery.Text(failure.Message))
	}
	return r.SendTo(ctx, delivery.Target{}, outputs)
}

func (r *Runtime) StartStream(context.Context) (delivery.MessageStream, error) {
	return localMessageStream{runtime: r, id: nextStreamID.Add(1)}, nil
}

func (r *Runtime) PresentStatus(ctx context.Context, status clishare.Status) error {
	if !r.interactive {
		_, err := fmt.Fprintln(os.Stdout, FormatCompact(status.Snapshot, time.Now(), 0))
		return err
	}
	return r.enqueue(ctx, tuiStatusMsg(status.Snapshot))
}

func (r *Runtime) PresentNotice(ctx context.Context, notice clishare.Notice) error {
	if !r.interactive {
		_, err := fmt.Fprintln(os.Stdout, "[notice] "+notice.Text)
		return err
	}
	return r.enqueue(ctx, tuiNoticeMsg{Text: notice.Text, Level: noticeLevel(notice.Level)})
}

func (r *Runtime) TargetID() string { return r.name + ":local" }

func (r *Runtime) enqueue(ctx context.Context, msg tea.Msg) error {
	select {
	case r.output <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-r.done:
		return io.ErrClosedPipe
	}
}

type localMessageStream struct {
	runtime *Runtime
	id      uint64
}

func (s localMessageStream) Send(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
	if !s.runtime.interactive {
		if event.Type != delivery.StreamSend {
			return delivery.Receipt{}, nil
		}
		text := event.Text
		if len(event.Blocks) > 0 {
			text = delivery.ContentBlocksText(event.Blocks)
		}
		_, err := fmt.Fprintln(os.Stdout, strings.TrimSuffix(text, "\n"))
		return delivery.Receipt{}, err
	}
	return delivery.Receipt{}, s.runtime.enqueue(ctx, tuiStreamMsg{ID: s.id, Event: event})
}

func noticeLevel(text string) slog.Level {
	var level slog.Level
	if err := level.UnmarshalText([]byte(text)); err != nil {
		return slog.LevelInfo
	}
	return level
}
