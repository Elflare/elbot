package modelmgr

import (
	"context"
	"elbot/internal/config"
	"elbot/internal/llm"
	"fmt"
	"github.com/pelletier/go-toml/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func writeProviders(t *testing.T, path string, file config.ProviderFile) {
	t.Helper()
	data, err := toml.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestReloadProvidersPublishesCompleteSnapshotAndRetainsSelection(t *testing.T) {
	opts := testOptions()
	opts.Metadata.DefaultContextWindow = 100
	s := newTestService(t, opts)
	s.providersPath = filepath.Join(t.TempDir(), "providers.toml")
	s.configDir = filepath.Dir(s.providersPath)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[{"id":"new"}]}`) }))
	defer server.Close()
	file := config.ProviderFile{Providers: map[string]config.ProviderConfig{"p": {APIMode: "response", BaseURL: server.URL, Models: []string{"new"}}}, ModelMetadata: config.ModelMetadataConfig{DefaultContextWindow: 200}}
	writeProviders(t, s.providersPath, file)
	old := s.ResolveMode("work")
	before := s.snapshot()
	if err := s.ReloadProviders(t.Context()); err != nil {
		t.Fatal(err)
	}
	current := s.ResolveMode("work")
	if current.Client == old.Client || current.Origin.APIType != llm.APITypeResponse || current.ContextWindow(t.Context()) != 200 {
		t.Fatal("incomplete snapshot")
	}
	if old.Origin.APIType != llm.APITypeChat || old.ContextWindow(t.Context()) != 100 {
		t.Fatal("old snapshot changed")
	}
	if !reflect.DeepEqual(before, s.snapshot()) {
		t.Fatal("reload changed selections")
	}
	if got := s.ResolveMode("chat"); got.Provider != "q" || got.Client != nil {
		t.Fatal("removed selection changed")
	}
	if err := s.ValidateSelection(config.ModelSelection{Provider: "q", Model: "z"}); err == nil {
		t.Fatal("removed provider accepted")
	}
	if got := s.ModelList("", ModelListOptions{}); len(got.Options) != 1 || got.Options[0].Model != "new" {
		t.Fatal(got)
	}
	for _, stage := range []string{"read", "build", "cancel"} {
		ctx := t.Context()
		switch stage {
		case "read":
			if err := os.WriteFile(s.providersPath, []byte("invalid = ["), 0600); err != nil {
				t.Fatal(err)
			}
		case "build":
			file.Providers["p"] = config.ProviderConfig{Proxy: "://invalid"}
			writeProviders(t, s.providersPath, file)
		case "cancel":
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			cancel()
		}
		if err := s.ReloadProviders(ctx); err == nil {
			t.Fatalf("%s accepted", stage)
		}
		if got := s.ResolveMode("work"); got.Client != current.Client || got.Origin != current.Origin {
			t.Fatalf("%s partially published", stage)
		}
	}
}
func TestReloadIsIndependentOfOldCatalogFetch(t *testing.T) {
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	opts := testOptions()
	opts.Clients["p"] = &testClient{list: func(context.Context) ([]string, error) { close(started); <-release; return []string{"stale"}, nil }}
	s := newTestService(t, opts)
	s.providersPath = filepath.Join(t.TempDir(), "providers.toml")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[{"id":"fresh"}]}`) }))
	defer server.Close()
	writeProviders(t, s.providersPath, config.ProviderFile{Providers: map[string]config.ProviderConfig{"p": {BaseURL: server.URL}}})
	go func() { s.ModelList("", ModelListOptions{}); close(done) }()
	waitSignal(t, started)
	if err := s.ReloadProviders(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := s.ModelList("", ModelListOptions{}); len(got.Options) != 1 || got.Options[0].Model != "fresh" {
		t.Fatal(got)
	}
	unblock()
	waitSignal(t, done)
	if got := s.ModelList("", ModelListOptions{}); len(got.Options) != 1 || got.Options[0].Model != "fresh" {
		t.Fatal("old fetch overwrote cache")
	}
}
func TestConcurrentReloadsAndReadersUseMatchingProviderFacts(t *testing.T) {
	servers := make([]*httptest.Server, 2)
	for i := range servers {
		n := i
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"data":[{"id":"a","context_window":%d}]}`, 100+n)
		}))
		defer servers[i].Close()
	}
	s := newTestService(t, testOptions())
	s.providersPath = filepath.Join(t.TempDir(), "providers.toml")
	write := func(i int) {
		writeProviders(t, s.providersPath, config.ProviderFile{Providers: map[string]config.ProviderConfig{"p": {BaseURL: servers[i].URL, ModelConfigs: map[string]config.ModelConfig{"a": {ContextWindow: 100 + i}}}}})
	}
	write(0)
	if err := s.ReloadProviders(t.Context()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			for j := 0; j < 30; j++ {
				selected := s.ResolveMode("work")
				want := 100
				if selected.Origin.BaseURL == servers[1].URL {
					want = 101
				}
				if selected.ContextWindow(t.Context()) != want {
					t.Error("mixed provider facts")
				}
				s.ModelList("", ModelListOptions{})
			}
		})
	}
	for i := 0; i < 20; i++ {
		write(i % 2)
		if err := s.ReloadProviders(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
