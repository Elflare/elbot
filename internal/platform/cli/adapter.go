// Package cli 是 CLI 平台入口，负责对外能力与本地、远程实现的装配。
package cli

import (
	"context"
	"os"
	"sync"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/cancelreader"

	"elbot/internal/completion"
)

// Adapter is a CLI platform adapter that reads from stdin.
type Adapter struct {
	name                       string // 根部登记的平台名。
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
	id      uint64 // Identity of this message's preview, shared by all its events.
}

var nextStreamID atomic.Uint64
