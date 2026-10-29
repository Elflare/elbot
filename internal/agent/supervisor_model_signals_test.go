package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/modelmgr"
	"elbot/internal/platform/dispatch"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
)

type retryNoticePlatform struct {
	assemblyPlatform
	retryNotices chan string
}

func (p *retryNoticePlatform) SendTo(ctx context.Context, noticeTarget delivery.Target, noticeOutputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	text := delivery.FallbackOutput(noticeOutputs).Text
	if strings.Contains(text, "正在重试") {
		info, _ := contextinfo.ConversationFromContext(ctx)
		p.retryNotices <- info.Source.ScopeID
	}
	return p.assemblyPlatform.SendTo(ctx, noticeTarget, noticeOutputs)
}

func TestSharedRetrySubscriptionCoversChatCompactAndNaming(t *testing.T) {
	deps := isolatedModuleDeps(t)
	cfg := isolatedAgentConfig()
	cfg.Context = config.Default().Context
	cfg.Session.Naming.TriggerStep = 1
	calls := make(chan context.Context, 4)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1)%2 == 1 {
			http.Error(w, "retryable", 429)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"summary and title"},"finish_reason":"stop"}]}

data: [DONE]

`)
	}))
	t.Cleanup(server.Close)
	p := &retryNoticePlatform{retryNotices: make(chan string, 5)}
	models, err := modelmgr.NewModelManager(t.Context(), &config.Config{Providers: map[string]config.ProviderConfig{"p": {BaseURL: server.URL, Models: []string{"m"}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}, LLMRequest: config.LLMRequestConfig{MaxRetries: 1, RetryInitialDelaySeconds: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer models.Close(context.Background())
	connection, err := models.ObserveModelRetrying(func(_ context.Context, e modelmgr.ModelRetryingEvent) error { calls <- e.CallContext; return nil }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	deps.Models = models
	sender := dispatch.New(dispatch.Options{Primary: p})
	deps.Platform = isolatedPlatformCapabilities{sender}
	connectTestNotifications(t, sender)
	runtime, err := NewSupervisor(t.Context(), cfg, deps)
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	if err != nil {
		t.Fatal(err)
	}
	ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli", ScopeID: "local"}, Identity: contextinfo.Identity{PlatformUserID: "local"}})
	ctx = contextinfo.WithModel(ctx, contextinfo.Model{Provider: "p", Model: "m", APIType: "chat"})
	namingDone := make(chan struct{}, 1)
	logs, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.EventName == "session_naming_completed" && record.Category == events.LogRuntime {
			namingDone <- struct{}{}
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logs.Close(context.Background()) })
	for _, name := range []string{"chat", "compact", "naming"} {
		result := make(chan error, 1)
		go func() {
			switch name {
			case "chat":
				result <- runtime.HandleMessage(ctx, MessageRequest{Text: "hello"})
			case "compact":
				err := runtime.HandleMessage(ctx, MessageRequest{Text: "/compact"})
				result <- err
			case "naming":
				row := &storage.Session{ID: storage.NewID(), OwnerID: "cli:local", Platform: "cli", PlatformScopeID: "local", Mode: storage.SessionModeWork, Title: "New session", Status: storage.SessionStatusActive}
				if err := deps.Store.Sessions().Create(ctx, row); err != nil {
					result <- err
					return
				}
				if err := deps.Store.Messages().Append(ctx, &storage.Message{SessionID: row.ID, Role: storage.RoleUser, Content: "hello"}); err != nil {
					result <- err
					return
				}
				if err := runtime.Start(); err != nil {
					result <- err
					return
				}
				runtime.sessions.MaybeScheduleNaming(ctx, row.ID)
				<-namingDone
				result <- runtime.sessions.Close(context.Background())
			}
		}()
		var call context.Context
		select {
		case call = <-calls:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not invoke shared client", name)
		}
		select {
		case scope := <-p.retryNotices:
			wantScope := "local"
			if name == "naming" {
				wantScope = ""
			}
			if scope != wantScope {
				t.Fatal("lost original source", scope)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s retry never reached subscriber", name)
		}
		if name == "naming" {
			if _, ok := contextinfo.ConversationFromContext(call); ok {
				t.Fatal("naming retained the inbound conversation")
			}
		}
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not finish", name)
		}
		select {
		case <-call.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("call context remained alive")
		}
	}
	if len(p.retryNotices) != 0 {
		t.Fatal("duplicate retries")
	}

}
