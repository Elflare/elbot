package remote

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	platformshare "elbot/internal/platform/share"
	"elbot/internal/signal"
)

var _ platformshare.Runtime = (*RemoteServer)(nil)

func (s *RemoteServer) Start(ctx context.Context, handler platformshare.MessageHandler) (err error) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return signal.ErrClosed
	}
	if done := s.startDone; done != nil {
		s.mu.Unlock()
		select {
		case <-done:
			return s.startErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.startDone = make(chan struct{})
	s.workCtx, s.workCancel = context.WithCancel(ctx)
	s.work.Add(1)
	s.mu.Unlock()
	defer func() {
		if err != nil {
			s.workCancel()
		}
		s.startErr = err
		close(s.startDone)
		s.work.Done()
	}()
	listener, err := (&net.ListenConfig{}).Listen(s.workCtx, "tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", s.managementHandler())
	mux.HandleFunc("POST /cli/v1/files", s.handleMedia)
	mux.HandleFunc("GET /cli/v1/files/{id}", s.handleMedia)
	mux.HandleFunc("DELETE /cli/v1/files/{id}", s.handleMedia)
	mux.HandleFunc(defaultRemotePath, func(w http.ResponseWriter, req *http.Request) {
		s.handleWebSocket(s.transportCtx, handler, w, req)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || ctx.Err() != nil {
		_ = listener.Close()
		return signal.ErrClosed
	}
	s.listener, s.server = listener, server
	s.transportCtx, s.transportCancel = context.WithCancel(context.WithoutCancel(ctx))
	s.stopParent = context.AfterFunc(ctx, s.BeginClose)
	s.serveDone = make(chan struct{})
	go func() {
		serveErr := server.Serve(listener)
		s.mu.Lock()
		if !(s.listenerStopped && (errors.Is(serveErr, net.ErrClosed) || errors.Is(serveErr, http.ErrServerClosed))) {
			s.runErr = serveErr
		}
		close(s.serveDone)
		s.mu.Unlock()
		s.BeginClose()
	}()
	if s.frontendDir != "" {
		s.work.Add(1)
		go s.initFrontend(mux)
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
	serveDone := s.serveDone
	if s.workCancel != nil {
		s.workCancel()
	}
	if s.listener != nil {
		s.listenerStopped = s.listener.Close() == nil
	}
	s.mu.Unlock()
	go func() {
		if serveDone != nil {
			<-serveDone
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
