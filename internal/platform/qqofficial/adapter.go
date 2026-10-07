package qqofficial

import (
	"context"
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
	"elbot/internal/signal"
	"elbot/internal/storage"
)

type Adapter struct {
	cfg         Config
	store       storage.Store
	chatHistory storage.ChatHistoryRepository
	client      *apiClient

	connectedOnce sync.Once
	connected     *signal.Signal[platform.ConnectedEvent]

	seqMu     sync.Mutex
	seqByID   map[string]int
	wsWriteMu sync.Mutex
}

func New(cfg Config, store storage.Store, chatHistory storage.ChatHistoryRepository) *Adapter {
	applyDefaults(&cfg)
	return &Adapter{cfg: cfg, store: store, chatHistory: chatHistory, client: newAPIClient(cfg), seqByID: map[string]int{}}
}

func (a *Adapter) Name() string { return platformName }

func (a *Adapter) Enabled() bool { return a.cfg.Enabled }

func (a *Adapter) ConnectedSignal() *signal.Signal[platform.ConnectedEvent] {
	a.connectedOnce.Do(func() {
		a.connected = signal.New[platform.ConnectedEvent](a.Name() + ".connected")
	})
	return a.connected
}

func (a *Adapter) notifyConnected(ctx context.Context) {
	// Emit records dispatch failures; accepted callbacks run on app-owned queues.
	_ = a.ConnectedSignal().Emit(ctx, platform.ConnectedEvent{Platform: a.Name()})
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
				_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
					Category: globalevents.LogRuntime,
					Level:    slog.LevelWarn,
					Name:     "qqofficial_gateway_disconnected",
					Module:   "qqofficial",
					Summary:  "qqofficial gateway disconnected",
					Fields:   []slog.Attr{slog.Any("error", err), slog.Any("reconnect_mode", reason.mode.String())},
				})
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
		sent, err := a.sendOutputs(ctx, target, outputs)
		receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, sent.PlatformMessageIDs...)
		receipt.SentMessages = append(receipt.SentMessages, sent.SentMessages...)
		if err != nil {
			return receipt, err
		}
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
	info, ok := contextinfo.ConversationFromContext(ctx)
	return ok && info.Source.Platform == platformName && info.Source.ConversationKind == contextinfo.ConversationGroup
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
