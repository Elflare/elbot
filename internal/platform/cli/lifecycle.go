// lifecycle.go 管理本地 CLI 接入与终端资源的分阶段退出。
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"

	"elbot/internal/completion"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
	"github.com/muesli/cancelreader"
)

var _ share.Runtime = (*Adapter)(nil)

func (a *Adapter) Start(ctx context.Context, handler share.MessageHandler) (err error) {
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return signal.ErrClosed
	}
	if a.started {
		done := a.startDone
		a.mu.Unlock()
		select {
		case <-done:
			return a.startErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	a.started = true
	a.workCtx, a.workCancel = context.WithCancel(ctx)
	renderCtx, renderCancel := context.WithCancel(context.WithoutCancel(ctx))
	a.renderCancel = renderCancel
	a.stopParent = context.AfterFunc(ctx, a.BeginClose)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.startErr = err
		close(a.startDone)
		a.mu.Unlock()
		if err != nil {
			a.BeginClose()
		}
	}()
	entry := localMessageHandler{next: handler, adapter: a}
	submit := func(_ context.Context, text string) error {
		if !a.admit() {
			return signal.ErrClosed
		}
		defer a.work.Done()
		return entry.HandleMessage(a.workCtx, text)
	}
	if !a.interactive {
		close(a.renderDone)
		info, statErr := a.input.Stat()
		if statErr != nil {
			a.finishInput(statErr)
			return statErr
		}
		var reader io.Reader = a.input
		if !info.Mode().IsRegular() {
			cancellable, readErr := cancelreader.NewReader(a.input)
			if readErr != nil {
				a.finishInput(readErr)
				return readErr
			}
			a.mu.Lock()
			a.inputReader = cancellable
			if a.closing {
				cancellable.Cancel()
			}
			a.mu.Unlock()
			reader = cancellable
		}
		go func() {
			scanner := bufio.NewScanner(reader)
			fmt.Print("> ")
			for scanner.Scan() {
				if a.workCtx.Err() != nil {
					break
				}
				text := scanner.Text()
				if text == "/exit" {
					break
				}
				_ = submit(a.workCtx, text)
				fmt.Print("> ")
			}
			scanErr := scanner.Err()
			if errors.Is(scanErr, cancelreader.ErrCanceled) && a.workCtx.Err() != nil {
				scanErr = nil
			}
			a.finishInput(scanErr)
		}()
	} else {
		ready := make(chan struct{})
		go func() {
			runErr := runTUI(tuiModel{ctx: renderCtx, inputCtx: a.workCtx, handler: submit, completion: a.complete, output: a.output, userName: a.userName, assistantName: a.assistantName, ready: func() { close(ready) }, stop: a.BeginClose}, a.setProgram)
			a.finishInput(runErr)
			close(a.renderDone)
		}()
		select {
		case <-ready:
		case <-a.renderDone:
			a.mu.Lock()
			err = a.runErr
			a.mu.Unlock()
			return err
		case <-a.workCtx.Done():
			return a.workCtx.Err()
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	a.notifyConnected(ctx)
	return nil
}

// admit 与 BeginClose 共用锁，确保 Wait 开始后不再登记新工作。
func (a *Adapter) admit() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		return false
	}
	a.work.Add(1)
	return true
}

func (a *Adapter) complete(_ context.Context, request completion.Request) ([]completion.Item, error) {
	if !a.admit() {
		return nil, signal.ErrClosed
	}
	defer a.work.Done()
	a.mu.Lock()
	complete := a.completion
	a.mu.Unlock()
	if complete == nil {
		return nil, nil
	}
	return complete(a.workCtx, request)
}

func (a *Adapter) finishInput(err error) {
	a.mu.Lock()
	a.runErr = errors.Join(a.runErr, err)
	if !a.inputStopped {
		a.inputStopped = true
		close(a.inputDone)
	}
	a.mu.Unlock()
	a.BeginClose()
}

func (a *Adapter) BeginClose() {
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return
	}
	a.closing = true
	started := a.started
	if a.workCancel != nil {
		a.workCancel()
	}
	if a.inputReader != nil {
		a.inputReader.Cancel()
	}
	if a.interactive || !started {
		if !a.inputStopped {
			a.inputStopped = true
			close(a.inputDone)
		}
	}
	if !started {
		close(a.renderDone)
	}
	a.mu.Unlock()
	go func() {
		if started {
			<-a.startDone
		}
		<-a.inputDone
		a.work.Wait()
		close(a.done)
	}()
}

// WaitExit 观察本地退出信号，回调收尾仍由 Wait 等待。
func (a *Adapter) WaitExit(ctx context.Context) error {
	select {
	case <-a.inputDone:
	case <-ctx.Done():
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := errors.Join(a.startErr, a.runErr); err != nil {
		return err
	}
	if a.inputStopped {
		return nil
	}
	return ctx.Err()
}

func (a *Adapter) Wait(ctx context.Context) error {
	select {
	case <-a.done:
	default:
		select {
		case <-a.done:
		case <-ctx.Done():
			select {
			case <-a.done:
			default:
				a.mu.Lock()
				err := a.runErr
				a.mu.Unlock()
				if err != nil {
					return err
				}
				return ctx.Err()
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runErr
}

func (a *Adapter) Close(ctx context.Context) error {
	a.BeginClose()
	select {
	case <-a.done:
	default:
		select {
		case <-a.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	a.mu.Lock()
	if a.renderCancel != nil {
		a.renderCancel()
	}
	a.mu.Unlock()
	select {
	case <-a.renderDone:
	default:
		select {
		case <-a.renderDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		a.closed = true
		if a.inputReader != nil {
			a.closeErr = a.inputReader.Close()
		}
		if a.stopParent != nil {
			a.stopParent()
		}
	}
	return a.closeErr
}
