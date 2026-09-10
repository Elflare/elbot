package agent

import "context"

// CurrentSessionID exposes the selected session without creating or resuming one.
func (a *Agent) CurrentSessionID(ctx context.Context) string {
	current, err := a.sessions.Current(ctx, a.scope(ctx))
	if err != nil {
		return ""
	}
	return current.ID
}
