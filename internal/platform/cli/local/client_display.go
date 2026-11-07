package local

import (
	"context"
	tea "github.com/charmbracelet/bubbletea"

	"elbot/internal/completion"
	"elbot/internal/platform/cli/share"
)

type clientDisplay struct{}

func NewClientDisplay() share.ClientDisplay { return clientDisplay{} }

func (clientDisplay) Run(ctx context.Context, userName string, handler func(context.Context, string) error, complete func(context.Context, completion.Request) ([]completion.Item, error), events <-chan share.DisplayEvent) error {
	output := make(chan tea.Msg)
	go func() {
		defer close(output)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				var msg tea.Msg
				switch event.Kind {
				case share.DisplayChat:
					msg = tuiOutputMsg(event.Text)
				case share.DisplayNotice:
					msg = tuiNoticeMsg{Text: event.Text, Level: noticeLevel(event.Level)}
				case share.DisplayStatus:
					msg = tuiStatusMsg(event.Status)
				case share.DisplayStream:
					msg = tuiStreamMsg{ID: event.StreamID, Event: event.Stream}
				default:
					continue
				}
				select {
				case output <- msg:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return runTUI(tuiModel{
		ctx: ctx, inputCtx: ctx, handler: handler, completion: complete,
		output: output, userName: userName, assistantName: "assistant",
	}, nil)
}

var _ share.ClientDisplay = clientDisplay{}
