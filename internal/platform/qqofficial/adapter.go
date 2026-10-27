// adapter.go 定义适配器及借用依赖；TODO：裁剪历史能力，运行职责迁入 lifecycle.go，发送与入站扩展分别归入 send.go、message.go。
// 本包后续仅保留 config.go 的 NewFromPlatformConfig 构造入口，旧 New、Run 待删除。

package qqofficial

import (
	"context"
	"elbot/internal/platform/share"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/platform"
	"elbot/internal/storage"
)

type Adapter struct {
	referenceResolver func(context.Context, share.ReferenceOptions) (share.ReferenceResult, error) // 根部注入的引用恢复能力。
	cfg               Config
	chatHistory       storage.ChatHistoryRepository // TODO：适配器子包改造时替换为 AdapterDeps.ChatHistory 的窄能力。
	client            *apiClient

	seqMu     sync.Mutex
	seqByID   map[string]int
	wsWriteMu sync.Mutex
}

// TODO：删除旧 New，将初始化并入 config.go 的 NewFromPlatformConfig，不保留兼容入口。
func New(cfg Config, chatHistory storage.ChatHistoryRepository) *Adapter {
	applyDefaults(&cfg)
	return &Adapter{cfg: cfg, chatHistory: chatHistory, client: newAPIClient(cfg), seqByID: map[string]int{}}
}

func (a *Adapter) Name() string { return platformName }

func (a *Adapter) Enabled() bool { return a.cfg.Enabled }

func (a *Adapter) notifyConnected(ctx context.Context) {
	// Subscribers hand background work to their own business queues.
	_, _ = globalevents.PlatformConnected.Emit(ctx, globalevents.PlatformConnectedEvent{Platform: a.Name()})
}

// TODO：删除旧 Run，由 lifecycle.go 的 Start、BeginClose、Wait、Close 接替运行协调。
func (a *Adapter) Run(ctx context.Context, handler platform.PlatformHandler) error {
	if !a.cfg.Enabled {
		return nil
	}
	state := gatewayState{}
	backoff := share.NewBackoff(a.cfg.reconnectInterval(), 10*time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		reason, err := a.runGatewayOnce(ctx, handler, &state)
		if err != nil && !errors.Is(err, context.Canceled) {
			if backoff.ShouldWarn() {
				{
					record := globalevents.LogRecord{
						Category:     globalevents.LogRuntime,
						Level:        slog.LevelWarn,
						EventName:    "qqofficial_gateway_disconnected",
						Module:       "qqofficial",
						Type:         globalevents.ContentText,
						Content:      fmt.Sprintf("qqofficial gateway disconnected\nerror=%v\nreconnect_mode=%v", err, reason.mode.String()),
						ContentLimit: 4096,
						ResultStatus: globalevents.ResultFailed,
					}
					facts, _ := contextinfo.ExecutionFromContext(ctx)
					if facts.SessionID != "" {
						record.Identity.SessionID = &facts.SessionID
					}
					if facts.RunID != "" {
						record.Identity.RunID = &facts.RunID
					}
					if facts.Attempt != "" {
						record.Identity.Attempt = &facts.Attempt
					}
					if facts.RequestID != "" {
						record.Identity.RequestID = &facts.RequestID
					}
					if facts.RootRequestID != "" {
						record.Identity.RootRequestID = &facts.RootRequestID
					}
					_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
				}
			}
		} else {
			backoff.Reset()
		}
		if reason.fatal {
			if err != nil {
				return err
			}
			return fmt.Errorf("qqofficial gateway stopped")
		}
		if !sleepContext(ctx, backoff.Delay()) {
			return ctx.Err()
		}
	}
}

func (a *Adapter) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	return a.sendContextOutput(ctx, outputs)
}

func (a *Adapter) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	if target.Empty() {
		return a.SendChat(ctx, outputs)
	}
	targets, err := a.targets(target)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if err == context.Canceled {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return delivery.Receipt{}, logResult, err
	}
	var receipt delivery.Receipt
	for _, target := range targets {
		target.Proactive = true
		sent, logResult, err := a.sendOutputs(ctx, target, outputs)
		receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, sent.PlatformMessageIDs...)
		receipt.SentMessages = append(receipt.SentMessages, sent.SentMessages...)
		if err != nil {
			return receipt, logResult, err
		}
	}
	return receipt, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (a *Adapter) targets(target delivery.Target) ([]sendTarget, error) {
	if platformName := strings.TrimSpace(target.Platform); platformName != "" && platformName != a.Name() {
		return nil, fmt.Errorf("qqofficial cannot send to platform %q", platformName)
	}
	if target.Superadmins {
		targets := make([]sendTarget, 0, len(a.cfg.Superadmins))
		for _, id := range a.cfg.Superadmins {
			id = strings.TrimSpace(strings.TrimPrefix(id, platformName+":"))
			if id != "" {
				targets = append(targets, sendTarget{Kind: targetC2C, OpenID: id})
			}
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("qqofficial superadmins are not configured")
		}
		return targets, nil
	}
	if id := strings.TrimSpace(target.PrivateUserID); id != "" {
		return []sendTarget{{Kind: targetC2C, OpenID: id}}, nil
	}
	if id := strings.TrimSpace(target.GroupID); id != "" {
		return []sendTarget{{Kind: targetGroup, OpenID: id}}, nil
	}
	scope := strings.TrimSpace(target.ScopeID)
	if strings.HasPrefix(scope, "c2c:") {
		return []sendTarget{{Kind: targetC2C, OpenID: strings.TrimPrefix(scope, "c2c:")}}, nil
	}
	if strings.HasPrefix(scope, "group:") {
		return []sendTarget{{Kind: targetGroup, OpenID: strings.TrimPrefix(scope, "group:")}}, nil
	}
	return nil, fmt.Errorf("qqofficial target missing private_user_id, group_id or scope_id")
}

func (a *Adapter) nextMsgSeq(msgID string) int {
	msgID = strings.TrimSpace(msgID)
	if msgID == "" {
		return 0
	}
	a.seqMu.Lock()
	defer a.seqMu.Unlock()
	a.seqByID[msgID]++
	return a.seqByID[msgID]
}

type sendTarget struct {
	Kind      sendTargetKind
	OpenID    string
	MsgID     string
	EventID   string
	Proactive bool
}

type sendTargetKind string

const (
	targetC2C   sendTargetKind = "c2c"
	targetGroup sendTargetKind = "group"
)

// messageData is immutable after the inbound Info is published. Message and
// conversation IDs live in the common fields, not in this extension.
type messageData struct {
	EventID   string
	EventType string
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
