package execution

import (
	"context"
	"strings"
	"sync"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/storage"
	toolshare "elbot/internal/tool/share"
)

// runBatch 由协调器持有，worker 只写自己的调用槽，完成后通过 ready 交回。
type runBatch struct {
	manager           *Manager
	deps              toolshare.RunnerDeps
	req               toolshare.RunRequest
	sessionID         string
	ctx               context.Context // 当前协调器上下文，接管后随 RefreshExecution 刷新。
	cancel            context.CancelFunc
	workers           sync.WaitGroup // 只等待归批次所有的同步调用。
	calls             []batchCall
	ready             chan int // 已完成调用的原始位置，决定提交顺序。
	completed         int
	callsPublished    bool
	elyphRulesShown   bool
	confirmationExtra []string
}

type batchCall struct {
	call       llm.ToolCallRequest // 准备 Hook 成功后替换为实际调用。
	actor      contextinfo.Actor
	risk       string
	startedAt  time.Time
	finishedAt time.Time
	result     executionResult
	published  bool // 结果已经提交并发布，退出时无需补发。
}

// Run 顺序准备与确认、并发执行，并由单一协调器提交结果。
func (m *Manager) Run(ctx context.Context, deps toolshare.RunnerDeps, req toolshare.RunRequest) (out toolshare.RunResult) {
	if req.Session.Mode == storage.SessionModeChat {
		return toolshare.RunResult{}
	}
	state, err := m.DecodeState(req.Session.Metadata)
	if err != nil {
		return toolshare.RunResult{Err: err}
	}
	ctx, cancel := context.WithCancel(ctx)
	b := runBatch{
		manager: m, deps: deps, req: req, sessionID: req.Session.ID,
		ctx: ctx, cancel: cancel, elyphRulesShown: state.ElyphRulesShown,
		calls: make([]batchCall, len(req.Calls)), ready: make(chan int, len(req.Calls)),
	}
	for i, call := range req.Calls {
		b.calls[i].call = call
	}
	defer func() {
		b.close()
		out.ConfirmationExtra = strings.Join(b.confirmationExtra, "\n\n")
	}()
	head := deps.ToolCallMessage(b.sessionID, req.AssistantText, req.AssistantRawText, req.ReasoningContent, req.Calls)
	if err := req.Committer.Begin(ctx, &head); err != nil {
		return toolshare.RunResult{Err: err}
	}
	for index := range b.calls {
		if err := b.ctx.Err(); err != nil {
			return toolshare.RunResult{Stopped: true, Err: err}
		}
		ctx, err := deps.RefreshExecution(b.ctx, req.Session)
		if err != nil {
			return toolshare.RunResult{Err: err}
		}
		b.ctx = ctx
		if index == 0 {
			deps.PublishToolCalls(ctx, req.Calls, strings.TrimSpace(req.AssistantText) != "")
			b.callsPublished = true
		}
		if result := b.startCall(index); result.Stopped || result.Err != nil {
			return result
		}
	}
	for b.completed < len(b.calls) {
		select {
		case index := <-b.ready:
			if err := b.commit(index, false); err != nil {
				return toolshare.RunResult{Err: err}
			}
		case <-b.ctx.Done():
			return toolshare.RunResult{Stopped: true, Err: b.ctx.Err()}
		}
	}
	return toolshare.RunResult{Err: req.Committer.Flush(b.ctx)}
}

func (b *runBatch) close() {
	b.cancel()
	b.workers.Wait()
	if !b.callsPublished {
		return
	}
	for _, task := range b.calls {
		if task.published {
			continue
		}
		segments := task.result.Message.Segments
		if task.result.ExecutionStarted {
			segments = llm.TextSegments("tool outcome is unknown: result has not been saved")
		} else if len(segments) == 0 {
			segments = llm.TextSegments("tool call was not executed")
		}
		b.deps.PublishToolResult(context.WithoutCancel(b.ctx), task.call.ID, delivery.ToolResult{
			Success:  false,
			Segments: segments,
		})
	}
}

// await 在准备或确认阻塞时提交其他已完成的调用。
func (b *runBatch) await(run func()) error {
	done := make(chan struct{})
	go func() { defer close(done); run() }()
	for {
		select {
		case <-done:
			return nil
		case index := <-b.ready:
			if err := b.commit(index, false); err != nil {
				b.cancel()
				<-done
				return err
			}
		case <-b.ctx.Done():
			<-done
			return b.ctx.Err()
		}
	}
}
