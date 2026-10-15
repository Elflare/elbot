package modelmgr

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"
)

func TestReloadProvidersPublishesCompleteSnapshotAndRetainsSelection(t *testing.T) {
	opts := testOptions()
	opts.Metadata.DefaultContextWindow = 100
	file := config.ProviderFile{Providers: map[string]config.ProviderConfig{"p": {APIMode: "response", BaseURL: "https://new.invalid", Models: []string{"new"}}}, ModelMetadata: config.ModelMetadataConfig{DefaultContextWindow: 200}}
	var loadErr, buildErr error
	opts.LoadProviders = func(context.Context) (config.ProviderFile, error) { return file, loadErr }
	opts.BuildClient = func(_ string, _ config.ProviderConfig) (llm.Client, error) {
		if buildErr != nil {
			return nil, buildErr
		}
		return &testClient{}, nil
	}
	s := newTestService(t, opts)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	old := s.ResolveMode("work")
	if old.ContextWindow(t.Context()) != 100 {
		t.Fatal("initial metadata missing")
	}
	before := s.snapshot()
	if err := s.ReloadProviders(t.Context()); err != nil {
		t.Fatal(err)
	}
	current := s.ResolveMode("work")
	if current.Client == old.Client || current.Origin.APIType != llm.APITypeResponse || current.Origin.BaseURL != "https://new.invalid" || current.ContextWindow(t.Context()) != 200 {
		t.Fatal("new snapshot is incomplete")
	}
	if old.Origin.APIType != llm.APITypeChat || old.ContextWindow(t.Context()) != 100 {
		t.Fatal("old snapshot changed")
	}
	if !reflect.DeepEqual(before, s.snapshot()) {
		t.Fatal("reload changed model selections")
	}
	if got := s.ResolveMode("chat"); got.Provider != "q" || got.Client != nil {
		t.Fatal("removed selection was rewritten or retained its client")
	}
	if err := s.ValidateSelection(config.ModelSelection{Provider: "q", Model: "z"}); err == nil {
		t.Fatal("removed provider remains usable")
	}
	result := s.ModelList("", ModelListOptions{})
	if len(result.Options) != 1 || result.Options[0].Model != "new" {
		t.Fatalf("catalog=%+v", result)
	}
	for _, stage := range []string{"load", "build", "cancel"} {
		ctx := t.Context()
		loadErr, buildErr = nil, nil
		switch stage {
		case "load":
			loadErr = errors.New("read failed")
		case "build":
			buildErr = errors.New("build failed")
		case "cancel":
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			cancel()
		}
		if err := s.ReloadProviders(ctx); err == nil {
			t.Fatalf("%s failure accepted", stage)
		}
		if got := s.ResolveMode("work"); got.Client != current.Client || got.Origin != current.Origin || got.ContextWindow(t.Context()) != 200 {
			t.Fatalf("%s failure partially published", stage)
		}
	}
}

func TestReloadIsIndependentOfOldCatalogFetch(t *testing.T) {
	opts := testOptions()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	opts.Clients["p"] = &testClient{list: func(context.Context) ([]string, error) { close(started); <-release; return []string{"stale"}, nil }}
	opts.LoadProviders = func(context.Context) (config.ProviderFile, error) {
		return config.ProviderFile{Providers: map[string]config.ProviderConfig{"p": {}}}, nil
	}
	opts.BuildClient = func(string, config.ProviderConfig) (llm.Client, error) {
		return &testClient{list: func(context.Context) ([]string, error) { return []string{"fresh"}, nil }}, nil
	}
	s := newTestService(t, opts)
	t.Cleanup(func() { unblock(); waitSignal(t, done); _ = s.Close(context.Background()) })
	go func() { s.ModelList("", ModelListOptions{}); close(done) }()
	waitSignal(t, started)
	if err := s.ReloadProviders(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := s.ModelList("", ModelListOptions{}); len(got.Options) != 1 || got.Options[0].Model != "fresh" {
		t.Fatalf("new catalog=%+v", got)
	}
	unblock()
	waitSignal(t, done)
	if got := s.ModelList("", ModelListOptions{}); len(got.Options) != 1 || got.Options[0].Model != "fresh" {
		t.Fatal("old fetch overwrote new catalog")
	}
}

func TestConcurrentReloadsAndReadersUseMatchingProviderFacts(t *testing.T) {
	opts := testOptions()
	var generation atomic.Int32
	opts.LoadProviders = func(context.Context) (config.ProviderFile, error) {
		n := int(generation.Add(1))
		return config.ProviderFile{Providers: map[string]config.ProviderConfig{"p": {BaseURL: fmt.Sprint(n), ModelConfigs: map[string]config.ModelConfig{"a": {ContextWindow: n}}}}}, nil
	}
	opts.BuildClient = func(_ string, cfg config.ProviderConfig) (llm.Client, error) {
		return &testClient{list: func(context.Context) ([]string, error) { return []string{cfg.BaseURL}, nil }}, nil
	}
	s := newTestService(t, opts)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if err := s.ReloadProviders(t.Context()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				if err := s.ReloadProviders(t.Context()); err != nil {
					t.Error(err)
					return
				}
				selected := s.ResolveMode("work")
				ids, err := selected.Client.ListModels(t.Context())
				if err != nil || len(ids) != 1 || ids[0] != selected.Origin.BaseURL || fmt.Sprint(selected.ContextWindow(t.Context())) != selected.Origin.BaseURL {
					t.Error("mixed provider generations")
					return
				}
				s.ModelList("", ModelListOptions{})
			}
		}()
	}
	wg.Wait()
}
