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
	cfg    Config
	store  storage.Store
	client *apiClient
	logger Logger

	notify func(context.Context, string)

	seqMu     sync.Mutex
	seqByID   map[string]int
	wsWriteMu sync.Mutex
}

func New(cfg Config, store storage.Store, logger Logger) *Adapter {
	applyDefaults(&cfg)
	return &Adapter{cfg: cfg, store: store, client: newAPIClient(cfg), logger: logger, seqByID: map[string]int{}}
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

func (a *Adapter) SendNotice(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	if target.Empty() {
		return a.SendChat(ctx, outputs)
	}
	openIDs, err := a.targetOpenIDs(target)
	if err != nil {
		return delivery.Receipt{}, err
	}
	var receipt delivery.Receipt
	for _, openID := range openIDs {
		ctx := context.WithValue(ctx, targetKey{}, sendTarget{OpenID: openID, Proactive: true})
		sent, err := a.sendContextOutput(ctx, outputs)
		if err != nil {
			return delivery.Receipt{}, err
		}
		receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, sent.PlatformMessageIDs...)
	}
	return receipt, nil
}

func (a *Adapter) targetOpenIDs(target delivery.Target) ([]string, error) {
	if platformName := strings.TrimSpace(target.Platform); platformName != "" && platformName != a.Name() {
		return nil, fmt.Errorf("qqofficial cannot send to platform %q", platformName)
	}
	if target.Superadmins {
		ids := make([]string, 0, len(a.cfg.Superadmins))
		for _, id := range a.cfg.Superadmins {
			id = strings.TrimSpace(strings.TrimPrefix(id, platformName+":"))
			if id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("qqofficial superadmins are not configured")
		}
		return ids, nil
	}
	if id := strings.TrimSpace(target.PrivateUserID); id != "" {
		return []string{id}, nil
	}
	if scope := strings.TrimSpace(target.ScopeID); strings.HasPrefix(scope, "c2c:") {
		return []string{strings.TrimPrefix(scope, "c2c:")}, nil
	}
	return nil, fmt.Errorf("qqofficial target missing private_user_id or c2c scope_id")
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
	OpenID    string
	MsgID     string
	EventID   string
	Proactive bool
}

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
