package modelmgr

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"
)

func TestCatalogCachesErrorsAndRefreshesIndependentProviders(t *testing.T) {
	var calls atomic.Int32
	opts := testOptions()
	opts.Clients["p"] = &testClient{list: func(context.Context) ([]string, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("provider unavailable")
		}
		return []string{"remote", "b", "remote"}, nil
	}}
	opts.Providers["missing"] = config.ProviderConfig{BaseURL: "https://invalid.example", APIKeyEnv: "MISSING_KEY", Models: []string{"configured"}}
	opts.Clients["missing"] = &testClient{list: func(context.Context) ([]string, error) { t.Error("missing key provider called"); return nil, nil }}
	s := newTestService(t, opts)
	first := s.ModelList("", ModelListOptions{})
	if len(first.Options) != 6 || len(first.Errors) != 2 {
		t.Fatalf("first = %#v", first)
	}
	if first.Errors[0].Provider != "missing" || !strings.Contains(first.Errors[0].Err.Error(), "MISSING_KEY") || first.Errors[1].Provider != "p" {
		t.Fatalf("errors = %#v", first.Errors)
	}
	s.ModelList("p", ModelListOptions{})
	if calls.Load() != 1 {
		t.Fatal("cached failure fetched again")
	}
	fresh := s.ModelList("p", ModelListOptions{Fresh: true})
	if calls.Load() != 2 || len(fresh.Options) != 4 || len(fresh.Errors) != 1 {
		t.Fatalf("fresh = %#v, calls=%d", fresh, calls.Load())
	}
	if fresh.Options[0].Index != 2 || fresh.Options[3].Model != "remote" {
		t.Fatalf("filtered indices/order = %#v", fresh.Options)
	}
	if _, err := selectTestModel(s, "work", "5"); err != nil {
		t.Fatal(err)
	}
	if got := s.ResolveMode("work"); got.Model != "remote" {
		t.Fatalf("global index resolved to %#v", got)
	}
}

func TestCatalogCachesEmptyResultsUntilRefresh(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"success", nil}, {"failure", errors.New("provider unavailable")}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s := newTestService(t, testModelOptions{
				Providers:  map[string]config.ProviderConfig{"p": {}},
				ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}},
				Clients: map[string]llm.Client{"p": &testClient{list: func(context.Context) ([]string, error) {
					if calls.Add(1) == 1 {
						return nil, tc.err
					}
					return []string{"remote"}, nil
				}}},
			})
			for range 2 {
				got := s.ModelList("", ModelListOptions{})
				if len(got.Options) != 0 || calls.Load() != 1 {
					t.Fatalf("empty cache = %#v, calls=%d", got, calls.Load())
				}
				if tc.err == nil {
					if len(got.Errors) != 0 {
						t.Fatalf("unexpected errors: %v", got.Errors)
					}
				} else if len(got.Errors) != 1 || got.Errors[0].Provider != "p" || !errors.Is(got.Errors[0].Err, tc.err) {
					t.Fatalf("cached error lost: %v", got.Errors)
				}
			}
			for _, fresh := range []bool{true, false} {
				got := s.ModelList("", ModelListOptions{Fresh: fresh})
				if calls.Load() != 2 || len(got.Errors) != 0 || len(got.Options) != 1 || got.Options[0].Model != "remote" {
					t.Fatalf("refreshed cache = %#v, calls=%d", got, calls.Load())
				}
			}
		})
	}
}

func TestCatalogFetchesProvidersConcurrently(t *testing.T) {
	opts := testOptions()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan struct{})
	t.Cleanup(func() { close(release); waitSignal(t, done) })
	client := &testClient{list: func(ctx context.Context) ([]string, error) {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []string{"remote"}, nil
	}}
	opts.Clients = map[string]llm.Client{"p": client, "q": client}
	opts.Providers["q"] = config.ProviderConfig{BaseURL: "https://invalid.example", APIKey: "key"}
	s := newTestService(t, opts)
	go func() { s.ModelList("", ModelListOptions{}); close(done) }()
	// Both providers must start before either is allowed to finish.
	waitSignal(t, started)
	waitSignal(t, started)
	// Cleanup releases provider calls, even when a barrier assertion fails.
}

func TestModelMatchingAndCatalogMarks(t *testing.T) {
	s := newTestService(t, testOptions())
	for _, tc := range []struct{ arg, want string }{{"0", "out of range"}, {"99", "out of range"}, {"a", "ambiguous"}, {"absent", "not found"}, {"", "usage:"}} {
		if _, err := selectTestModel(s, "work", tc.arg); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("select %q = %v, want %s", tc.arg, err, tc.want)
		}
	}
	if _, err := selectTestModel(s, "elwisp2", "Q/Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectCompactModel("p/b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectNamingModel("p/c"); err != nil {
		t.Fatal(err)
	}
	result := s.ModelList("", ModelListOptions{})
	for _, option := range result.Options {
		switch option.Provider + "/" + option.Model {
		case "p/a":
			if strings.Join(option.ModeMarks, ",") != "work,elwisp1,elwisp3" {
				t.Fatalf("work markers = %#v", option)
			}
		case "q/z":
			if strings.Join(option.ModeMarks, ",") != "chat,elwisp2" {
				t.Fatalf("slot markers = %#v", option)
			}
		case "p/b":
			if !option.Compact {
				t.Fatalf("compact marker = %#v", option)
			}
		case "p/c":
			if !option.Naming {
				t.Fatalf("naming marker = %#v", option)
			}
		}
	}
}
