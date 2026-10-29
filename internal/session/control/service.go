// 职责：定义会话管理组件、自有配置及所需依赖。
// 接收绑定协调、存储、活跃状态和前台接管校验能力。
package control

import (
	"context"
	"fmt"
	"sync"
	"time"

	"elbot/internal/session/share"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

type Config struct {
	DefaultMode    string               // 已由配置层设置默认值的前台模式。
	IdleExpiration IdleExpirationConfig // 前台绑定的闲置期限。
}

type ServiceDeps struct {
	Sessions interface {
		Create(context.Context, *storage.Session) error
		Get(context.Context, string) (*storage.Session, error)
		Mutate(context.Context, string, func(*storage.Session) error) (*storage.Session, error)
		List(context.Context, storage.ListSessionsRequest) ([]storage.SessionSummary, error)
		Delete(context.Context, string) error
		ListExpiredIDs(context.Context, time.Time) ([]string, error)
		DeleteIfExpired(context.Context, string, time.Time) (bool, error)
	}
	Messages interface {
		ListBySession(context.Context, string) ([]storage.Message, error)
	}
	Bindings interface {
		share.BindingCoordinator
		EnterScope(context.Context, share.Scope) (context.Context, func(), error)
		EnterBinding(context.Context, *share.Binding) (context.Context, func(), error)
		ClearSessionBindings(context.Context, string)
	}
	ActiveSessionIDs func() []string                               // Request／Turn 的真实活跃事实，包含收尾阶段。
	CheckForeground  func(context.Context, *storage.Session) error // 准入内只读兼容性检查，不执行 I/O。
	IsSuperadmin     func(share.Scope) bool                        // 按绑定身份查询权限，不持有权限管理器。
	ReleaseSession   func(string)                                  // 准入内同步释放闲置会话状态；不得 I/O、等待或重新准入。
	ReleaseBinding   func(*share.Binding)                          // 准入内释放原绑定状态；与 ReleaseSession 一样须幂等。
}

type CreateRequest struct {
	ID       string // 可预分配目标 ID，以便一次取得源、目标准入。
	Title    string // 新会话标题。
	Mode     string // 空值使用已配置的默认模式。
	Metadata string // 初始元数据 JSON。
}

type ActivateModeRequest struct {
	Mode            string // 目标前台模式。
	NewSessionTitle string // 尚无当前会话时的新标题。
}

type ActivateModeResult struct {
	Session       *storage.Session // 已激活会话。
	AlreadyActive bool             // 调用前已为目标模式。
}

type Status struct {
	Session           *storage.Session // 当前会话快照。
	MessageCount      int              // 当前会话自己的消息数。
	LastUserContent   string           // 最近一条非空用户正文。
	LastAnswerContent string           // 最近一条非空助手正文。
}

type BackgroundRequest struct {
	SessionID string            // 非空时复用未接管的后台会话。
	Kind      string            // 后台来源种类。
	Name      string            // 后台任务名称。
	Title     string            // 新会话标题。
	Metadata  map[string]string // 后台业务附加字段。
}

// Service 的业务修改经过 Binding 准入；mu 只保护计时索引和启停状态。
// 构造依赖由根部检查。外部调用退出后才能 Close，不关闭借用的仓库或 Binding。
type Service struct {
	deps                ServiceDeps
	cfg                 Config
	mu                  sync.Mutex
	idle                map[string]map[*share.Binding]*idleBinding // Session 到绑定生命周期记录；不缓存存储行。
	started             bool                                       // 已启用调度，不允许停止后重启。
	closed              bool                                       // 不再接纳调度及事件订阅。
	runCtx              context.Context                            // 定时任务运行上下文，不继承触发请求。
	cancel              context.CancelFunc                         // 取消定时回调的准入等待。
	stopParent          func() bool                                // 解除运行上下文的停止通知。
	timers              sync.WaitGroup                             // 含已排期及已触发但尚未退出的 timer。
	closeOnce           sync.Once
	done                chan struct{} // 定时回调与接管观察者真实退出。
	foregroundActivated *signal.Signal[ForegroundActivatedEvent, signal.Empty]
}

var _ share.BindingCoordinator = (*Service)(nil)

func NewService(cfg Config, deps ServiceDeps) *Service {
	return &Service{deps: deps, cfg: cfg, idle: make(map[string]map[*share.Binding]*idleBinding), done: make(chan struct{}),
		foregroundActivated: signal.New[ForegroundActivatedEvent, signal.Empty]("session.foreground_activated")}
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
	if s.started {
		return nil
	}
	s.started = true
	s.runCtx, s.cancel = context.WithCancel(ctx)
	s.stopParent = context.AfterFunc(ctx, s.BeginClose)
	for _, bindings := range s.idle {
		for binding, state := range bindings {
			s.scheduleLocked(binding, state)
		}
	}
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
	for _, bindings := range s.idle {
		for _, state := range bindings {
			s.stopTimerLocked(state)
		}
	}
}

func (s *Service) Close(ctx context.Context) error {
	s.BeginClose()
	s.closeOnce.Do(func() {
		go func() {
			s.timers.Wait()
			// 无期限关闭等待实际观察回调退出，只会成功返回。
			_ = s.foregroundActivated.Close(context.Background())
			s.mu.Lock()
			s.idle = nil
			s.mu.Unlock()
			close(s.done)
		}()
	})
	select {
	case <-s.done:
		return nil
	default:
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) Done() <-chan struct{} { return s.done }

func (s *Service) EnterSessions(ctx context.Context, ids ...string) (context.Context, func(), error) {
	return s.deps.Bindings.EnterSessions(ctx, ids...)
}

func (s *Service) EnterActivation(ctx context.Context, scope share.Scope, ids ...string) (context.Context, func(), error) {
	return s.deps.Bindings.EnterActivation(ctx, scope, ids...)
}

func (s *Service) CurrentBinding(scope share.Scope) *share.Binding {
	return s.deps.Bindings.CurrentBinding(scope)
}

func (s *Service) requireIdle(id string) error {
	for _, active := range s.deps.ActiveSessionIDs() {
		if active == id {
			return share.ErrSessionBusy
		}
	}
	return nil
}

// enterSession 使用上下文中的原绑定，或仅取 Session 准入；身份校验不在本层重复。
func (s *Service) enterSession(ctx context.Context, id string) (context.Context, func(), error) {
	if binding, ok := share.BindingFromContext(ctx); ok {
		if binding.SessionID() != id {
			return nil, nil, fmt.Errorf("session binding mismatch")
		}
		return s.deps.Bindings.EnterBinding(ctx, binding)
	}
	return s.EnterSessions(ctx, id)
}
