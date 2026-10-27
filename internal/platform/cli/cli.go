// cli.go 实现 CLI 适配器运行、输入接入与消息发送。

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"
	"github.com/muesli/cancelreader"

	"elbot/internal/completion"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
)

// Adapter is a CLI platform adapter that reads from stdin.
type Adapter struct {
	mu                         sync.Mutex
	output                     chan tea.Msg
	program                    *tea.Program
	userName                   string
	assistantName              string
	completion                 func(context.Context, completion.Request) ([]completion.Item, error)
	input                      *os.File                  // 借用输入，不负责关闭 os.Stdin。
	interactive                bool                      // 构造时确定的终端模式。
	started                    bool                      // 已有一次启动尝试，禁止重新运行。
	closing                    bool                      // 接入闸门已关闭。
	closed                     bool                      // 终端与输入辅助资源已释放。
	inputStopped               bool                      // inputDone 是否已发布。
	startDone                  chan struct{}             // 本地初始化结果已发布。
	inputDone                  chan struct{}             // 扫描退出或 TUI 业务输入停止。
	renderDone                 chan struct{}             // 终端程序及读取协程已退出。
	done                       chan struct{}             // 接入与已登记业务回调均退出。
	startErr, runErr, closeErr error                     // 初始化、运行、清理结果分别保留。
	workCtx                    context.Context           // 业务输入与补全的运行上下文。
	workCancel                 context.CancelFunc        // 停止业务输入与已接纳回调。
	renderCancel               context.CancelFunc        // 最终请求终端正常退出。
	inputReader                cancelreader.CancelReader // 可取消的输入包装，不持有底层文件所有权。
	stopParent                 func() bool               // 解除上游运行取消的观察。
	work                       sync.WaitGroup            // 已接纳的输入和补全调用。

}

type cliMessageStream struct {
	adapter *Adapter
}

// New creates a new CLI adapter.
func New() *Adapter {
	return &Adapter{output: make(chan tea.Msg, 256), userName: "user", assistantName: "assistant", input: os.Stdin, interactive: isatty.IsTerminal(os.Stdin.Fd()), startDone: make(chan struct{}), inputDone: make(chan struct{}), renderDone: make(chan struct{}), done: make(chan struct{})}
}

// Name returns the platform name.
func (a *Adapter) Name() string {
	return "cli"
}

func (a *Adapter) SetCompleter(service func(context.Context, completion.Request) ([]completion.Item, error)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.completion = service
}

func (a *Adapter) setProgram(program *tea.Program) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.program = program
}

func (a *Adapter) notifyConnected(ctx context.Context) {
	// Subscribers hand background work to their own business queues.
	_, _ = globalevents.PlatformConnected.Emit(ctx, globalevents.PlatformConnectedEvent{Platform: a.Name()})
}

func (a *Adapter) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	if !a.interactive {
		return nil, fmt.Errorf("cli streaming output requires interactive TUI")
	}
	a.mu.Lock()
	program := a.program
	a.mu.Unlock()
	if program == nil {
		return nil, fmt.Errorf("cli TUI is not running")
	}
	return cliMessageStream{adapter: a}, nil
}

func (s cliMessageStream) Append(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	return s.adapter.sendTUIMessage(tuiOutputMsg(text), text)
}

func (s cliMessageStream) Replace(ctx context.Context, text string) (delivery.Receipt, globalevents.LogResult, error) {
	if err := s.adapter.sendTUIMessage(tuiReplaceAssistantMsg(text), text); err != nil {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (s cliMessageStream) Finish(ctx context.Context) (delivery.Receipt, globalevents.LogResult, error) {
	if err := s.adapter.sendTUIMessage(tuiFinishAssistantMsg{}, "\n"); err != nil {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (a *Adapter) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	text := outputsText(outputs)
	if text != "" {
		if err := a.sendTUIMessage(tuiOutputMsg(text), text); err != nil {
			return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
		}
	}
	return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func outputsText(outputs []delivery.Output) string {
	return delivery.FallbackOutput(outputs).Text
}

func (a *Adapter) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	if platformName := strings.TrimSpace(target.Platform); platformName != "" && platformName != a.Name() {
		{
			err := fmt.Errorf("cli cannot send to platform %q", platformName)
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
			return delivery.Receipt{}, logResult, err
		}
	}
	text := outputsText(outputs)
	if text != "" {
		if err := a.sendTUIMessage(tuiOutputMsg(text), text); err != nil {
			return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
		}
	}
	return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (a *Adapter) sendTUIMessage(msg tea.Msg, fallback string) error {
	a.mu.Lock()
	output := a.output
	program := a.program
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return io.ErrClosedPipe
	}
	if !a.interactive {
		_, err := fmt.Fprintln(os.Stdout, strings.TrimSuffix(fallback, "\n"))
		return err
	}
	if program != nil {
		program.Send(msg)
		return nil
	}
	select {
	case output <- msg:
	default:
		_, err := fmt.Fprintln(os.Stdout, strings.TrimSuffix(fallback, "\n"))
		return err
	}
	return nil
}

func printLine(text string) {
	if text == "" {
		return
	}
	fmt.Print(text)
	if !strings.HasSuffix(text, "\n") {
		fmt.Println()
	}
}
