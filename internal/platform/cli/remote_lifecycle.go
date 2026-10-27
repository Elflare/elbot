// remote_lifecycle.go 将远程 CLI 接入停止与连接最终释放分开。
package cli

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"elbot/internal/platform/share"
	"elbot/internal/signal"
)

var _ share.Runtime = (*RemoteServer)(nil)

func (s *RemoteServer) Start(ctx context.Context, handler share.MessageHandler) (err error) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return signal.ErrClosed
	}
	if s.started {
		done := s.startDone
		s.mu.Unlock()
		select {
		case <-done:
			return s.startErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.started = true
	s.workCtx, s.workCancel = context.WithCancel(ctx)
	s.transportCtx, s.transportCancel = context.WithCancel(context.WithoutCancel(ctx))
	s.stopParent = context.AfterFunc(ctx, s.BeginClose)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.startErr = err
		close(s.startDone)
		s.mu.Unlock()
		if err != nil {
			s.BeginClose()
		}
	}()
	listener, err := (&net.ListenConfig{}).Listen(s.workCtx, "tcp", s.cfg.Server.Listen)
	if err != nil {
		s.mu.Lock()
		if s.workCtx.Err() == nil || !errors.Is(err, s.workCtx.Err()) {
			s.runErr = err
		}
		close(s.serveDone)
		s.mu.Unlock()
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc(defaultRemotePath, func(w http.ResponseWriter, req *http.Request) {
		s.handleWebSocket(s.transportCtx, handler, w, req)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	s.mu.Lock()
	s.listener, s.server = listener, server
	closing := s.closing
	if closing {
		s.listenerStopped = listener.Close() == nil
	}
	s.mu.Unlock()
	go func() {
		serveErr := server.Serve(listener)
		s.mu.Lock()
		if !(s.listenerStopped && (errors.Is(serveErr, net.ErrClosed) || errors.Is(serveErr, http.ErrServerClosed))) {
			s.runErr = errors.Join(s.runErr, serveErr)
		}
		close(s.serveDone)
		s.mu.Unlock()
		s.BeginClose()
	}()
	if closing {
		return signal.ErrClosed
	}
	return nil
}

func (s *RemoteServer) BeginClose() {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	s.closing = true
	started := s.started
	if s.workCancel != nil {
		s.workCancel()
	}
	if s.listener != nil {
		s.listenerStopped = s.listener.Close() == nil
	}
	s.mu.Unlock()
	go func() {
		if started {
			<-s.startDone
			<-s.serveDone
		}
		s.work.Wait()
		close(s.done)
	}()
}

func (s *RemoteServer) Wait(ctx context.Context) error {
	select {
	case <-s.done:
	default:
		select {
		case <-s.done:
		case <-ctx.Done():
			select {
			case <-s.done:
			default:
				s.mu.Lock()
				err := s.runErr
				s.mu.Unlock()
				if err != nil {
					return err
				}
				return ctx.Err()
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runErr
}

func (s *RemoteServer) Close(ctx context.Context) error {
	s.BeginClose()
	select {
	case <-s.done:
	default:
		select {
		case <-s.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	if !s.closingResources {
		s.closingResources = true
		go s.closeResources()
	}
	s.mu.Unlock()
	select {
	case <-s.closeDone:
	default:
		select {
		case <-s.closeDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeErr
}

func (s *RemoteServer) closeResources() {
	if s.transportCancel != nil {
		s.transportCancel()
	}
	var err error
	if s.server != nil {
		err = s.server.Close()
	}
	s.closeAll()
	s.transports.Wait()
	if s.stopParent != nil {
		s.stopParent()
	}
	s.mu.Lock()
	s.closeErr = err
	close(s.closeDone)
	s.mu.Unlock()
}
