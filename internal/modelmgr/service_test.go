package modelmgr

import (
	"context"
	"elbot/internal/llm/chatcompletions"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/llm"
)

type testClient struct {
	list func(context.Context) ([]string, error)
}

func (c *testClient) Stream(context.Context, chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	return nil, errors.New("unexpected chat request")
}

func (c *testClient) ListModels(ctx context.Context) ([]string, error) {
	if c.list != nil {
		return c.list(ctx)
	}
	return nil, nil
}

func testOptions() Options {
	return Options{
		Clients:     map[string]llm.Client{"p": &testClient{}, "q": &testClient{}},
		Providers:   map[string]config.ProviderConfig{"p": {Models: []string{"a", "b", "c"}}, "q": {Models: []string{"a", "z"}}},
		ModeModels:  map[string]config.ModelSelection{"work": {Provider: "p", Model: "a"}, "chat": {Provider: "q", Model: "z"}},
		DefaultMode: "chat",
	}
}

func newTestService(t *testing.T, opts Options) *Service {
	t.Helper()
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestValidateTaskSelectionDoesNotChangeSharedModels(t *testing.T) {
	s := newTestService(t, testOptions())
	before := s.ResolveMode("work")
	for _, invalid := range []config.ModelSelection{{}, {Provider: "p"}, {Model: "a"}, {Provider: "missing", Model: "a"}} {
		if err := s.ValidateSelection(invalid); err == nil {
			t.Fatalf("accepted %+v", invalid)
		}
	}
	if err := s.ValidateSelection(config.ModelSelection{Provider: "q", Model: "custom-task-model"}); err != nil {
		t.Fatal(err)
	}
	if got := s.ResolveMode("work"); got != before {
		t.Fatalf("task choice mutated work: %+v", got)
	}
}

func TestNewValidatesSelectionsAndOwnsInputMaps(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*Options)
	}{
		{"work", "mode_models.work", func(o *Options) { delete(o.ModeModels, "work") }},
		{"client", `client not found for provider "p"`, func(o *Options) { delete(o.Clients, "p") }},
		{"provider", `provider "missing" not found`, func(o *Options) { o.ModeModels["chat"] = config.ModelSelection{Provider: "missing", Model: "a"} }},
		{"naming", "naming_model provider/model", func(o *Options) { o.NamingModel.Model = "a" }},
		{"compact", "compact_model provider/model", func(o *Options) { o.CompactModel.Provider = "p" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := testOptions()
			tc.change(&opts)
			if _, err := New(opts); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New error = %v, want %q", err, tc.want)
			}
		})
	}
	opts := testOptions()
	client := opts.Clients["p"]
	s := newTestService(t, opts)
	delete(opts.Clients, "p")
	opts.ModeModels["work"] = config.ModelSelection{Provider: "q", Model: "z"}
	opts.Providers["p"].Models[0] = "mutated"
	delete(opts.Providers, "q")
	if got := s.ResolveMode("work"); got.Model != "a" || got.Client != client {
		t.Fatalf("selection changed with input maps: %#v", got)
	}
	if got := s.ModelList("", ModelListOptions{}); len(got.Options) != 5 || got.Options[0].Model != "a" {
		t.Fatalf("catalog changed with input maps: %#v", got)
	}
}

func TestSelectionsPersistAndRestoreWithFallbacks(t *testing.T) {
	opts := testOptions()
	opts.StatePath = filepath.Join(t.TempDir(), "state.toml")
	s := newTestService(t, opts)
	work := s.ResolveMode("work")
	if got := s.ResolveMode("elwisp2"); got != work {
		t.Fatalf("unconfigured slot = %#v", got)
	}
	if got := s.ResolveCompact(s.ResolveMode("chat")); got.Model != "z" {
		t.Fatalf("compact fallback = %#v", got)
	}
	if got := s.ResolveNaming(); got.Naming.Client != nil || got.Fallback != work {
		t.Fatalf("naming fallback = %#v", got)
	}
	for _, mode := range []string{"work", "chat", "elwisp1", "elwisp2", "elwisp3"} {
		if _, err := s.SelectModelForMode(mode, "p/b"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SelectCompactModel("p/c"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectNamingModel("q/z"); err != nil {
		t.Fatal(err)
	}
	state, err := config.LoadState(opts.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if state.Session.DefaultMode != "chat" {
		t.Fatalf("default mode lost: %#v", state.Session)
	}
	opts.ModeModels, opts.CompactModel, opts.NamingModel = state.ModeModels, state.CompactModel, state.NamingModel
	restored := newTestService(t, opts)
	for _, mode := range []string{"work", "chat", "elwisp1", "elwisp2", "elwisp3"} {
		if got := restored.ResolveMode(mode); got.Model != "b" {
			t.Fatalf("restored %s = %#v", mode, got)
		}
	}
	if got := restored.ResolveCompact(work); got.Model != "c" {
		t.Fatalf("compact override = %#v", got)
	}
	naming := restored.ResolveNaming()
	if naming.Naming.Model != "z" || naming.Fallback.Model != "b" {
		t.Fatalf("restored naming = %#v", naming)
	}
	// Old values remain useful snapshots after every mode has changed.
	if work.Model != "a" || work.Client != opts.Clients["p"] {
		t.Fatalf("old selection changed: %#v", work)
	}
}

func TestSaveFailureNeverPublishesAnySlot(t *testing.T) {
	for _, slot := range []string{"work", "chat", "elwisp1", "elwisp2", "elwisp3", "compact", "naming"} {
		t.Run(slot, func(t *testing.T) {
			opts := testOptions()
			opts.StatePath = filepath.Join(t.TempDir(), "state.toml")
			s := newTestService(t, opts)
			if err := s.commit(func(*runtimeState) {}); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(opts.StatePath)
			if err != nil {
				t.Fatal(err)
			}
			before := s.snapshot()
			failure := errors.New("disk full")
			s.save = func(string, config.StateConfig) error { return failure }
			switch slot {
			case "compact":
				_, err = s.SelectCompactModel("p/b")
			case "naming":
				_, err = s.SelectNamingModel("p/b")
			default:
				_, err = s.SelectModelForMode(slot, "p/b")
			}
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
			if !reflect.DeepEqual(s.snapshot(), before) {
				t.Fatalf("failed %s selection was published", slot)
			}
			after, err := os.ReadFile(opts.StatePath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(original) {
				t.Fatal("failed save changed original state file")
			}
		})
	}
}

func TestConcurrentCommitsPublishOnlySavedSnapshots(t *testing.T) {
	for _, failFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail-first-%t", failFirst), func(t *testing.T) {
			opts := testOptions()
			opts.StatePath = filepath.Join(t.TempDir(), "state.toml")
			s := newTestService(t, opts)
			s.ModelList("", ModelListOptions{})
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			var saves atomic.Int32
			failure := errors.New("write failed")
			s.save = func(path string, state config.StateConfig) error {
				if saves.Add(1) == 1 {
					close(entered)
					<-release
					if failFirst {
						return failure
					}
				}
				return config.SaveState(path, state)
			}
			first, second := make(chan error, 1), make(chan error, 1)
			go func() { _, err := s.SelectModelForMode("work", "p/b"); first <- err }()
			waitSignal(t, entered)
			go func() { _, err := s.SelectNamingModel("p/c"); second <- err }()
			read := make(chan NamingSelection, 1)
			go func() { read <- s.ResolveNaming() }()
			select {
			case got := <-read:
				if got.Fallback.Model != "a" || got.Naming.Model != "" {
					t.Fatalf("unsaved state visible: %#v", got)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("state read blocked on disk I/O")
			}
			releaseOnce.Do(func() { close(release) })
			err := waitError(t, first)
			if failFirst && !errors.Is(err, failure) || !failFirst && err != nil {
				t.Fatalf("first commit = %v", err)
			}
			if err := waitError(t, second); err != nil {
				t.Fatal(err)
			}
			state, err := config.LoadState(opts.StatePath)
			if err != nil {
				t.Fatal(err)
			}
			wantWork := "b"
			if failFirst {
				wantWork = "a"
			}
			if state.ModeModels["work"].Model != wantWork || state.NamingModel.Model != "c" {
				t.Fatalf("lost update or leaked failed update: %#v", state)
			}
			if got := s.ResolveNaming(); got.Fallback.Model != wantWork || got.Naming.Model != "c" {
				t.Fatalf("disk/memory mismatch: %#v", got)
			}
		})
	}
}

func TestConcurrentReadersSwitchesAndCatalogRefresh(t *testing.T) {
	s := newTestService(t, testOptions())
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 12; j++ {
				if i%3 == 0 {
					if _, err := s.SelectModelForMode("work", []string{"p/a", "p/b"}[j%2]); err != nil {
						t.Error(err)
					}
				} else {
					result := s.ModelList("", ModelListOptions{Fresh: j%2 == 0})
					if len(result.Options) != 5 {
						t.Errorf("catalog length = %d", len(result.Options))
						return
					}
					// Mutating returned slices must not corrupt any other caller.
					result.Options[0].Model = "caller mutation"
					if len(result.Options[0].ModeMarks) > 0 {
						result.Options[0].ModeMarks[0] = "caller mutation"
					}
					naming := s.ResolveNaming()
					if naming.Fallback.Client == nil || naming.Fallback.Model != "a" && naming.Fallback.Model != "b" {
						t.Errorf("invalid snapshot: %#v", naming)
					}
				}
			}
		}()
	}
	wg.Wait()
}

func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for barrier")
	}
}

func waitError(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for operation")
		return nil
	}
}

func (c *testClient) Protocol() llm.ProtocolID { return llm.ProtocolChat }
func (c *testClient) GenerateText(ctx context.Context, req llm.TextRequest) (llm.TextResult, error) {
	messages := []llm.LLMMessage{}
	if req.Instructions != "" {
		messages = append(messages, llm.LLMMessage{Role: llm.RoleSystem, Segments: llm.TextSegments(req.Instructions)})
	}
	messages = append(messages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments(req.Input)})
	chunks, err := c.Stream(ctx, chatcompletions.Request{Model: req.Model, Messages: messages, MaxTokens: req.MaxOutputTokens, ExtraBody: req.ExtraBody})
	if err != nil {
		return llm.TextResult{}, err
	}
	result := llm.TextResult{}
	for chunk := range chunks {
		if chunk.Error != nil {
			return llm.TextResult{}, chunk.Error
		}
		result.Text += chunk.DeltaContent
		if chunk.Usage != nil {
			result.Usage = chunk.Usage
		}
	}
	return result, ctx.Err()
}
