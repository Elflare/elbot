package naming

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
	"github.com/pelletier/go-toml/v2"
)

func TestTitleGeneratorUsesConfiguredOrContextModelProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, current, configured, target string
	}{
		{"chat fallback", "chat", "", "chat"},
		{"responses fallback", "response", "", "response"},
		{"responses dialogue with chat naming", "response", "chat", "chat"},
		{"chat dialogue with responses naming", "chat", "response", "response"},
		{"configured chat without context model", "", "chat", "chat"},
		{"configured responses without context model", "", "response", "response"},
		{"no configured or context model", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type request struct {
				path string
				body map[string]any
			}
			requests := make(chan request, 2)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				requests <- request{r.URL.Path, body}
				w.Header().Set("Content-Type", "text/event-stream")
				switch r.URL.Path {
				case "/chat/completions":
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"generated title\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				case "/responses":
					fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"title-response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"title-message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"generated title\"}]}]}}\n\n")
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
				}
			}))
			defer srv.Close()
			opts := config.Config{
				Providers: map[string]config.ProviderConfig{
					"chat":     {BaseURL: srv.URL, Models: []string{"dialogue-chat", "title-chat"}},
					"response": {BaseURL: srv.URL, APIMode: "response", Models: []string{"work-response", "dialogue-response", "title-response"}},
				},
				ModeModels:   map[string]config.ModelSelection{"work": {Provider: "response", Model: "work-response"}},
				CompactModel: config.ModelSelection{Provider: "response", Model: "work-response"},
			}
			if tc.configured != "" {
				opts.NamingModel = config.ModelSelection{Provider: tc.configured, Model: "title-" + tc.configured}
			}
			models, err := modelmgr.NewModelManager(t.Context(), &opts)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = models.Close(context.Background()) })
			ctx := t.Context()
			if tc.current != "" {
				ctx = contextinfo.WithModel(ctx, contextinfo.Model{Provider: tc.current, Model: "dialogue-" + tc.current, APIType: tc.current})
			}
			service := &Service{deps: ServiceDeps{ResolveNaming: models.ResolveNaming}}
			result, err := service.generateTitle(ctx, []storage.Message{{Role: storage.RoleUser, Content: "name this conversation"}})
			if tc.target == "" {
				if err == nil || len(requests) != 0 {
					t.Fatalf("missing model fell back to work: result=%+v err=%v requests=%d", result, err, len(requests))
				}
				return
			}
			model := "dialogue-" + tc.target
			if tc.configured != "" {
				model = "title-" + tc.target
			}
			if err != nil || result.Provider != tc.target || result.Model != model || result.RawTitle != "generated title" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if len(requests) != 1 {
				t.Fatalf("request count=%d", len(requests))
			}
			got := <-requests
			path := "/chat/completions"
			if tc.target == "response" {
				path = "/responses"
				if got.body["store"] != false {
					t.Fatalf("naming response was stored: %+v", got.body)
				}
			}
			if got.path != path || got.body["model"] != model {
				t.Fatalf("request=%+v", got)
			}
			for _, field := range []string{"tools", "previous_response_id"} {
				if _, exists := got.body[field]; exists {
					t.Errorf("independent naming request contains %s", field)
				}
			}
		})
	}
}

func TestNamingFallbackUsesSnapshotAcrossSelectionAndReload(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	requests := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"data":[{"id":"title"},{"id":"first"},{"id":"second"}]}`)
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		requests <- r.URL.Path + ":" + body.Model
		if r.URL.Path == "/naming/chat/completions" {
			close(started)
			<-release
			http.Error(w, "naming failed", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", body.Model+" title")
	}))
	defer srv.Close()
	defer unblock()
	dir := t.TempDir()
	cfg := &config.Config{ConfigPath: filepath.Join(dir, "config.toml"), ProvidersConfigPath: filepath.Join(dir, "providers.toml"), StateConfigPath: filepath.Join(dir, "state.toml"),
		Providers: map[string]config.ProviderConfig{
			"naming": {BaseURL: srv.URL + "/naming", Models: []string{"title"}},
			"old":    {BaseURL: srv.URL + "/old", Models: []string{"first"}},
			"next":   {BaseURL: srv.URL + "/next", Models: []string{"second"}},
		}, NamingModel: config.ModelSelection{Provider: "naming", Model: "title"}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "old", Model: "first"}}}
	models, err := modelmgr.NewModelManager(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer models.Close(context.Background())
	s := &Service{deps: ServiceDeps{ResolveNaming: models.ResolveNaming}}
	ctx := contextinfo.WithModel(t.Context(), contextinfo.Model{Provider: "old", Model: "first", APIType: "chat"})
	results := make(chan TitleResult, 1)
	errs := make(chan error, 1)
	go func() {
		result, err := s.generateTitle(ctx, []storage.Message{{Role: storage.RoleUser, Content: "user"}})
		results <- result
		errs <- err
	}()
	receive(t, started)
	if _, err := models.CommitModelForMode("work", modelmgr.ModelOption{Provider: "next", Model: "second"}); err != nil {
		t.Fatal(err)
	}
	if _, err := models.SelectNamingModel("next/second"); err != nil {
		t.Fatal(err)
	}
	provider := cfg.Providers["old"]
	provider.BaseURL = srv.URL + "/next"
	cfg.Providers["old"] = provider
	data, err := toml.Marshal(config.ProviderFile{Providers: cfg.Providers})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.ProvidersConfigPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := models.ReloadProviders(t.Context()); err != nil {
		t.Fatal(err)
	}
	unblock()
	result := receive(t, results)
	if err := receive(t, errs); err != nil || result.Provider != "old" || result.Model != "first" || result.RawTitle != "first title" {
		t.Fatalf("original=%+v %v", result, err)
	}
	if got := receive(t, requests); got != "/naming/chat/completions:title" {
		t.Fatal(got)
	}
	if got := receive(t, requests); got != "/old/chat/completions:first" {
		t.Fatal("fallback changed during request", got)
	}
	result, err = s.generateTitle(ctx, []storage.Message{{Role: storage.RoleUser, Content: "user"}})
	if err != nil || result.Provider != "next" || result.RawTitle != "second title" {
		t.Fatalf("new=%+v %v", result, err)
	}
}
