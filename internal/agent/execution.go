package agent

import (
	"context"
	"log/slog"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/platform"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
)

func (a *Agent) adoptForeground(ctx context.Context, row *storage.Session, binding *session.Binding) {
	if execution := a.turns.Execution(row.ID); execution != nil {
		ctx = security.WithActor(ctx, a.actor(ctx))
		execution.Adopt(session.WithBinding(ctx, binding))
	}
}

// Rebuild semantic values on the request context, retaining its cancellation.
func (a *Agent) executionContext(ctx context.Context) context.Context {
	e := turn.ExecutionFromContext(ctx)
	if e == nil {
		return ctx
	}
	foreground := e.Foreground()
	if foreground == nil {
		return ctx
	}
	if msg, ok := platform.MessageContextFrom(foreground); ok {
		ctx = platform.WithMessageContext(ctx, msg)
	}
	if actor, ok := security.ActorFromContext(foreground); ok {
		ctx = security.WithActor(ctx, actor)
	}
	if binding, ok := session.BindingFromContext(foreground); ok {
		ctx = session.WithBinding(ctx, binding)
	}
	ctx = tool.WithSandboxContext(ctx, tool.SandboxContext{})
	ctx = context.WithValue(ctx, cronModelSelectionKey{}, config.ModelSelection{})
	return ctx
}

func (a *Agent) refreshExecution(ctx context.Context, row *storage.Session) (context.Context, error) {
	ctx = a.executionContext(ctx)
	latest, err := a.store.Sessions().Get(ctx, row.ID)
	if err != nil {
		return ctx, err
	}
	*row = *latest
	return ctx, nil
}

// The sink changes when a running background execution is adopted. Already
// completed output is never replayed, and a request's cancellation is retained.
type executionTurnOutput struct {
	agent     *Agent
	execution *turn.Execution
	fallback  turnOutput
}

func (o executionTurnOutput) target(ctx context.Context) (context.Context, turnOutput) {
	if o.execution.Foreground() != nil {
		return o.agent.executionContext(ctx), foregroundTurnOutput{o.agent}
	}
	return ctx, o.fallback
}
func (o executionTurnOutput) StartStream(ctx context.Context) delivery.MessageStream {
	ctx, out := o.target(ctx)
	return out.StartStream(ctx)
}
func (o executionTurnOutput) FinishIntermediate(ctx, streamCtx context.Context, s delivery.MessageStream, text string, streaming bool) error {
	ctx, out := o.target(ctx)
	return out.FinishIntermediate(ctx, o.agent.executionContext(streamCtx), s, text, streaming)
}
func (o executionTurnOutput) ReplaceAndFinishStream(ctx, streamCtx context.Context, s delivery.MessageStream, text string) (delivery.Receipt, error) {
	ctx, out := o.target(ctx)
	return out.ReplaceAndFinishStream(ctx, o.agent.executionContext(streamCtx), s, text)
}
func (o executionTurnOutput) SendAssistant(ctx context.Context, text string) (delivery.Receipt, error) {
	ctx, out := o.target(ctx)
	if foreground := o.execution.Foreground(); foreground != nil {
		if parsed, err := background.ParseJSONResult(text); err == nil {
			text = parsed.Report
			if len(parsed.ReportSegments) > 0 {
				binding, _ := session.BindingFromContext(foreground)
				row, err := o.agent.store.Sessions().Get(ctx, binding.SessionID())
				if err != nil {
					return delivery.Receipt{}, err
				}
				dir := decodeSessionMetadata(row.Metadata).WorkspaceDir
				outputs, err := background.BuildReportOutputs("", parsed.ReportSegments, tool.SandboxContext{Root: dir, Dir: dir})
				if err != nil {
					return delivery.Receipt{}, err
				}
				receipt, err := out.SendAssistant(ctx, text)
				if err != nil {
					return receipt, err
				}
				return receipt, out.SendOutputs(ctx, outputs)
			}
		}
	}
	return out.SendAssistant(ctx, text)
}
func (o executionTurnOutput) SendOutputs(ctx context.Context, v []delivery.Output) error {
	ctx, out := o.target(ctx)
	return out.SendOutputs(ctx, v)
}
func (o executionTurnOutput) SendNotice(ctx context.Context, l slog.Level, text string) {
	ctx, out := o.target(ctx)
	out.SendNotice(ctx, l, text)
}
func (o executionTurnOutput) SendPreview(ctx context.Context, text string) {
	ctx, out := o.target(ctx)
	out.SendPreview(ctx, text)
}
func (o executionTurnOutput) SendReasoning(ctx context.Context, text string) {
	ctx, out := o.target(ctx)
	out.SendReasoning(ctx, text)
}
func (o executionTurnOutput) PublishRuntimeStatus(ctx context.Context, v runtimestatus.Snapshot) {
	ctx, out := o.target(ctx)
	out.PublishRuntimeStatus(ctx, v)
}

const foregroundInstructions = "此会话已由用户接管，当前是普通前台对话。保留原任务目标和历史，但后台无人值守、自动汇报及强制 JSON 输出要求已经解除；按当前用户要求正常回复，工具遵循前台权限和确认规则。"

func withForegroundInstructions(messages []llm.LLMMessage) []llm.LLMMessage {
	for _, message := range messages {
		if message.Role == llm.RoleSystem && llm.SegmentsTextOnly(message.Segments) == foregroundInstructions {
			return messages
		}
	}
	return append(messages, llm.LLMMessage{Role: llm.RoleSystem, Segments: llm.TextSegments(foregroundInstructions)})
}
