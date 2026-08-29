package qqofficial

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/platform"
	"elbot/internal/storage"
)

type Logger interface {
	DebugContext(context.Context, string, ...any)
	InfoContext(context.Context, string, ...any)
	WarnContext(context.Context, string, ...any)
	ErrorContext(context.Context, string, ...any)
}

type Adapter struct {
	cfg         Config
	store       storage.Store
	chatHistory storage.ChatHistoryRepository
	client      *apiClient
	logger      Logger

	notify func(context.Context, string)

	seqMu     sync.Mutex
	seqByID   map[string]int
	wsWriteMu sync.Mutex
}

func New(cfg Config, store storage.Store, chatHistory storage.ChatHistoryRepository, logger Logger) *Adapter {
	applyDefaults(&cfg)
	return &Adapter{cfg: cfg, store: store, chatHistory: chatHistory, client: newAPIClient(cfg), logger: logger, seqByID: map[string]int{}}
}

func (a *Adapter) Name() string { return platformName }

func (a *Adapter) Enabled() bool { return a.cfg.Enabled }

func (a *Adapter) SetConnectNotifier(notify func(context.Context, string)) {
	a.notify = notify
}

func (a *Adapter) notifyConnected(ctx context.Context) {
	if a.notify != nil {
		a.notify(ctx, a.Name())
	}
}

func (a *Adapter) Run(ctx context.Context, handler platform.PlatformHandler) error {
	if !a.cfg.Enabled {
		return nil
	}
	state := gatewayState{}
	backoff := platform.NewBackoff(a.cfg.reconnectInterval(), 10*time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		reason, err := a.runGatewayOnce(ctx, handler, &state)
		if err != nil && !errors.Is(err, context.Canceled) {
			if backoff.ShouldWarn() {
				a.logWarn(ctx, "qqofficial gateway disconnected", "error", err, "reconnect_mode", reason.mode.String())
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

func (a *Adapter) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	return a.sendContextOutput(ctx, outputs)
}

func (a *Adapter) SendNotice(ctx context.Context, notice delivery.Notice) (delivery.Receipt, error) {
	target := notice.Target
	outputs := notice.Outputs
	if target.Empty() && isGroupToolPreviewNotice(ctx, outputs) {
		return delivery.Receipt{}, nil
	}
	if target.Empty() {
		return a.SendChat(ctx, outputs)
	}
	targets, err := a.targets(target)
	if err != nil {
		return delivery.Receipt{}, err
	}
	var receipt delivery.Receipt
	for _, target := range targets {
		target.Proactive = true
		ctx := context.WithValue(ctx, targetKey{}, target)
		sent, err := a.sendContextOutput(ctx, outputs)
		if err != nil {
			return delivery.Receipt{}, err
		}
		receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, sent.PlatformMessageIDs...)
	}
	return receipt, nil
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

func isGroupToolPreviewNotice(ctx context.Context, outputs []delivery.Output) bool {
	if len(outputs) != 1 || outputs[0].Kind != delivery.KindText || !strings.HasPrefix(strings.TrimSpace(outputs[0].Text), "[tool]") {
		return false
	}
	target, ok := ctx.Value(targetKey{}).(sendTarget)
	return ok && target.Kind == targetGroup
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

func (a *Adapter) logDebug(ctx context.Context, msg string, attrs ...any) {
	if a.logger != nil {
		a.logger.DebugContext(ctx, msg, attrs...)
	}
}

func (a *Adapter) logInfo(ctx context.Context, msg string, attrs ...any) {
	if a.logger != nil {
		a.logger.InfoContext(ctx, msg, attrs...)
	}
}

func (a *Adapter) logWarn(ctx context.Context, msg string, attrs ...any) {
	if a.logger != nil {
		a.logger.WarnContext(ctx, msg, attrs...)
	} else {
		slog.WarnContext(ctx, msg, attrs...)
	}
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

type targetKey struct{}

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
