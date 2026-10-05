package agent

import (
	"context"
	"log/slog"

	"elbot/internal/background"
	"elbot/internal/delivery"
	runtimestatus "elbot/internal/runtime"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/session"
	"elbot/internal/turn"
)

// The sink changes when a running background execution is adopted. Already
// completed output is never replayed, and a request's cancellation is retained.
type executionTurnOutput struct {
	view       executionView
	foreground turnOutput
	sessions   *session.Service
	execution  *turn.Execution
	fallback   turnOutput
}

func (o executionTurnOutput) target(ctx context.Context) (context.Context, turnOutput) {
	if o.execution.Foreground() != nil {
		return o.view.Context(ctx), o.foreground
	}
	return ctx, o.fallback
}
func (o executionTurnOutput) StartStream(ctx context.Context) delivery.MessageStream {
	ctx, out := o.target(ctx)
	return out.StartStream(ctx)
}
func (o executionTurnOutput) FinishIntermediate(ctx, streamCtx context.Context, s delivery.MessageStream, text string, streaming bool) error {
	ctx, out := o.target(ctx)
	return out.FinishIntermediate(ctx, o.view.Context(streamCtx), s, text, streaming)
}
func (o executionTurnOutput) ReplaceAndFinishStream(ctx, streamCtx context.Context, s delivery.MessageStream, text string) (delivery.Receipt, error) {
	ctx, out := o.target(ctx)
	return out.ReplaceAndFinishStream(ctx, o.view.Context(streamCtx), s, text)
}
func (o executionTurnOutput) SendAssistant(ctx context.Context, text string) (delivery.Receipt, error) {
	ctx, out := o.target(ctx)
	if foreground := o.execution.Foreground(); foreground != nil {
		if parsed, err := background.ParseJSONResult(text); err == nil {
			text = parsed.Report
			if len(parsed.ReportSegments) > 0 {
				binding, _ := session.BindingFromContext(foreground)
				row, err := o.view.sessions.Get(ctx, binding.SessionID())
				if err != nil {
					return delivery.Receipt{}, err
				}
				dir, err := session.NewWorkspaceStore(o.sessions, o.view.sessions, row.ID).GetWorkspaceDir(ctx)
				if err != nil {
					return delivery.Receipt{}, err
				}
				outputs, err := background.BuildReportOutputs("", parsed.ReportSegments, sandboxctx.SandboxContext{Root: dir, Dir: dir})
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
