// 职责：定义命名组件、依赖、配置及每个会话的命名状态。
// 管理任务接纳、应用级取消与退出等待，使命名任务不随触发 Turn 结束。
package naming

import (
	"context"
	"errors"
	"sync"

	"elbot/internal/modelmgr"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

type Config struct {
	TriggerStep int // 配置层已验证的非空 user/assistant 消息触发数。
}

type ServiceDeps struct {
	Sessions interface {
		Get(context.Context, string) (*storage.Session, error)
		Mutate(context.Context, string, func(*storage.Session) error) (*storage.Session, error)
	}
	Messages interface {
		ListBySession(context.Context, string) ([]storage.Message, error)
	}
	ResolveNaming func(context.Context) modelmgr.NamingSelection
}

const maxNamingFailures = 3

type namingState struct {
	inFlight bool // 从准备占位到终态通知返回；由组件锁保护。
	retired  bool // ForgetSession 标记在途任务，真实收尾时删除，期间继续去重。
	done     bool // 持久化完成的内存快捷判断；在途期间仅任务读写。
	failures int  // 本次缓存生命期内的生成失败数，存储和准备失败不计入。
}

type Service struct {
	config         Config
	deps           ServiceDeps
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	stopParent     func() bool
	closed         bool
	namingStates   map[string]*namingState
	tasks          sync.WaitGroup // 同时覆盖同步准备、生成与终态通知。
	namingSignals  namingSignals
	logConnections []*signal.Connection
	closeOnce      sync.Once
	closeErr       error // 由唯一关闭流程固定，在 done 之后读取。
	done           chan struct{}
}

// NewService 只取得自有信号和订阅；部分失败仍交回组件供根部关闭。
func NewService(cfg Config, deps ServiceDeps) (*Service, error) {
	s := &Service{config: cfg, deps: deps, namingStates: make(map[string]*namingState), namingSignals: newNamingSignals(), done: make(chan struct{})}
	if err := s.connectLogSignals(); err != nil {
		s.BeginClose()
		return s, err
	}
	return s, nil
}

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return signal.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.ctx != nil {
		return nil
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.stopParent = context.AfterFunc(ctx, s.BeginClose)
	return nil
}

func (s *Service) BeginClose() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.stopParent != nil {
		s.stopParent()
	}
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *Service) Close(ctx context.Context) error {
	s.BeginClose()
	s.closeOnce.Do(func() {
		go func() {
			s.tasks.Wait()
			// Emit 的超时不代表回调退出；关闭信号和连接也必须等真实完成。
			var errs []error
			errs = append(errs, s.namingSignals.Scheduled.Close(context.Background()), s.namingSignals.Completed.Close(context.Background()))
			for _, connection := range s.logConnections {
				errs = append(errs, connection.Close(context.Background()))
			}
			s.mu.Lock()
			s.namingStates = nil
			s.logConnections = nil
			s.mu.Unlock()
			s.closeErr = errors.Join(errs...)
			close(s.done)
		}()
	})
	select {
	case <-s.done:
		return s.closeErr
	default:
	}
	select {
	case <-s.done:
		return s.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) Done() <-chan struct{} { return s.done }

// ForgetSession 仅修改内存，不等待、不重新准入，也不取消已经接纳的任务。
func (s *Service) ForgetSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state := s.namingStates[id]; state != nil {
		if state.inFlight {
			state.retired = true
		} else {
			delete(s.namingStates, id)
		}
	}
}

func (s *Service) beginNaming(id string) (context.Context, *namingState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx == nil || s.ctx.Err() != nil {
		return nil, nil
	}
	state := s.namingStates[id]
	if state == nil {
		state = &namingState{}
		s.namingStates[id] = state
	}
	if state.inFlight || state.done {
		return nil, nil
	}
	state.inFlight = true
	s.tasks.Add(1)
	return s.ctx, state
}

func (s *Service) finishNaming(id string, state *namingState) {
	s.mu.Lock()
	state.inFlight = false
	if state.retired {
		delete(s.namingStates, id)
	}
	s.mu.Unlock()
	s.tasks.Done()
}
