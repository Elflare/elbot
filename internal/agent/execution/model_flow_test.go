package execution

import (
	"fmt"

	"net/http"
	"net/http/httptest"

	"testing"
	"time"

	"elbot/internal/config"

	"elbot/internal/llm"
	"elbot/internal/modelmgr"

	"elbot/internal/storage"
)

func TestModelOptionsFetchesProvidersInParallel(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	newModelServer := func(model string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started <- model
			<-release
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"data":[{"id":%q}]}`, model)
		}))
	}
	srvA := newModelServer("remote-a")
	defer srvA.Close()
	srvB := newModelServer("remote-b")
	defer srvB.Close()
	defer close(release)

	providers := map[string]config.ProviderConfig{
		"local": {Models: []string{"local-model"}},
		"a":     {BaseURL: srvA.URL, APIKey: "test-key", Models: []string{"configured-a"}},
		"b":     {BaseURL: srvB.URL, APIKey: "test-key", Models: []string{"configured-b"}},
	}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "local", Model: "local-model"},
		storage.SessionModeChat: {Provider: "local", Model: "local-model"},
	}
	models := newTestModels(t, modelmgr.Options{
		Clients:     map[string]llm.Client{"local": &fakeLLM{models: []string{"local-model"}}},
		ModeModels:  modeModels,
		Providers:   providers,
		DefaultMode: storage.SessionModeWork,
	})

	done := make(chan []modelmgr.ModelOption, 1)
	go func() { done <- models.ModelList("", modelmgr.ModelListOptions{}).Options }()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for range 2 {
		select {
		case <-started:
		case <-deadline.C:
			t.Fatal("both model providers did not receive requests concurrently")
		}
	}
	// Reaching both handlers before either response proves the calls overlap.
	release <- struct{}{}
	release <- struct{}{}
	options := <-done
	got := []string{}
	for _, option := range options {
		got = append(got, option.Provider+"/"+option.Model)
	}
	want := []string{"a/configured-a", "a/remote-a", "b/configured-b", "b/remote-b", "local/local-model"}
	if !containsAll(got, want) {
		t.Fatalf("models = %#v, want to contain %#v", got, want)
	}
}

func TestModelOptionsCachesProviderModelsUntilFresh(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"id":"remote-%d"}]}`, requests)
	}))
	defer srv.Close()

	providers := map[string]config.ProviderConfig{
		"local":  {Models: []string{"local-model"}},
		"remote": {BaseURL: srv.URL, APIKey: "test-key"},
	}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "local", Model: "local-model"},
		storage.SessionModeChat: {Provider: "local", Model: "local-model"},
	}
	models := newTestModels(t, modelmgr.Options{
		Clients:     map[string]llm.Client{"local": &fakeLLM{models: []string{"local-model"}}},
		ModeModels:  modeModels,
		Providers:   providers,
		DefaultMode: storage.SessionModeWork,
	})

	first := models.ModelList("", modelmgr.ModelListOptions{})
	second := models.ModelList("", modelmgr.ModelListOptions{})
	fresh := models.ModelList("", modelmgr.ModelListOptions{Fresh: true})

	if requests != 2 {
		t.Fatalf("provider model requests = %d, want 2", requests)
	}
	if first.Options[0].Model != "local-model" || second.Options[0].Model != "local-model" || fresh.Options[0].Model != "local-model" {
		t.Fatalf("unexpected cached models: first=%#v second=%#v fresh=%#v", first.Options, second.Options, fresh.Options)
	}
}
