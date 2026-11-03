// Package hooks declares Agent-owned extension points. Providers register one
// callback group; rule execution and protocol adaptation belong to providers.
package hooks

import agentshare "elbot/internal/agent/share"

// Points 是共享的 Agent 回调契约。
type Points = agentshare.Points

// Facts 是共享的 Agent 事实快照。
type Facts = agentshare.Facts
