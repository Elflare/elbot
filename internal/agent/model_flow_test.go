package agent

import (
	"context"
	agentcommands "elbot/internal/agent/commands"
	"elbot/internal/config"
	"elbot/internal/platform"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckModelMarksCurrent(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{models: []string{"beta", "alpha"}}
	a := New(p, f, "beta", config.ProviderConfig{}, newTestStore(t))

	if err := a.HandleMessage(context.Background(), "/checkmodel"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	got := p.out.String()
	if !strings.Contains(got, "default:\n") || !strings.Contains(got, "* [2] beta (chat, work, elwisp1, elwisp2, elwisp3, compact)") {
		t.Fatalf("model marker missing from output:\n%s", got)
	}
}

func TestModelsGroupsProvidersAndSwitchPersistsState(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	statePath := filepath.Join(t.TempDir(), "state.toml")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"glm-4-flash"}]}`)
	}))
	defer srv.Close()
	providers := map[string]config.ProviderConfig{
		"deepseek": {Models: []string{"deepseek-chat"}},
		"zhipu":    {BaseURL: srv.URL, APIKey: "test-key", Models: []string{"glm-4-flash"}},
	}
	f := &fakeLLM{models: []string{"deepseek-chat"}}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "deepseek", Model: "deepseek-chat"},
		storage.SessionModeChat: {Provider: "zhipu", Model: "glm-4-flash"},
	}
	a := NewWithOptions(Options{Platform: p, Client: f, ModeModels: modeModels, Providers: providers, StatePath: statePath, Store: store, CommandPrefixes: []string{"/"}, SessionConfig: session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork}})

	if err := a.HandleMessage(context.Background(), "/models zhipu"); err != nil {
		t.Fatalf("models: %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "zhipu:\n") || !strings.Contains(got, "[2] glm-4-flash") {
		t.Fatalf("missing provider grouped model output: %q", got)
	}

	p.out.Reset()
	if err := a.HandleMessage(context.Background(), "/model 2"); err != nil {
		t.Fatalf("model switch: %v", err)
	}
	if !strings.Contains(p.out.String(), "switched to model: zhipu/glm-4-flash") {
		t.Fatalf("unexpected switch output: %q", p.out.String())
	}
	if a.CurrentProvider() != "zhipu" || a.CurrentModel() != "glm-4-flash" {
		t.Fatalf("current model = %s/%s", a.CurrentProvider(), a.CurrentModel())
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	state := string(data)
	if !strings.Contains(state, `[mode_models.work]`) || !strings.Contains(state, `provider = 'zhipu'`) || !strings.Contains(state, `model = 'glm-4-flash'`) {
		t.Fatalf("state not persisted: %s", state)
	}
	p.out.Reset()
	if err := a.HandleMessage(context.Background(), "/model --chat 1"); err != nil {
		t.Fatalf("chat model switch: %v", err)
	}
	if !strings.Contains(p.out.String(), "switched chat model: deepseek/deepseek-chat") {
		t.Fatalf("unexpected chat switch output: %q", p.out.String())
	}
	p.out.Reset()
	if err := a.HandleMessage(context.Background(), "/model --work 2"); err != nil {
		t.Fatalf("work model switch: %v", err)
	}
	if !strings.Contains(p.out.String(), "switched work model: zhipu/glm-4-flash") {
		t.Fatalf("unexpected work switch output: %q", p.out.String())
	}
	data, err = os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read mode state: %v", err)
	}
	state = string(data)
	if !strings.Contains(state, `[mode_models.chat]`) || !strings.Contains(state, `[mode_models.work]`) || !strings.Contains(state, `provider = 'deepseek'`) || !strings.Contains(state, `provider = 'zhipu'`) {
		t.Fatalf("mode state not persisted: %s", state)
	}
	p.out.Reset()
	if err := a.HandleMessage(context.Background(), "/models"); err != nil {
		t.Fatalf("models after mode switches: %v", err)
	}
	modelsOut := p.out.String()
	if !strings.Contains(modelsOut, "deepseek-chat (chat") || !strings.Contains(modelsOut, "glm-4-flash (work") || strings.Contains(modelsOut, "current") {
		t.Fatalf("models missing mode markers: %q", modelsOut)
	}
	p.out.Reset()
	if err := a.HandleMessage(context.Background(), "/model --compact 1"); err != nil {
		t.Fatalf("compact model switch: %v", err)
	}
	if !strings.Contains(p.out.String(), "switched compact model: deepseek/deepseek-chat") {
		t.Fatalf("unexpected compact switch output: %q", p.out.String())
	}
	data, err = os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read compact state: %v", err)
	}
	state = string(data)
	if !strings.Contains(state, `[compact_model]`) || !strings.Contains(state, `model = 'deepseek-chat'`) {
		t.Fatalf("compact state not persisted: %s", state)
	}
	p.out.Reset()
	if err := a.HandleMessage(context.Background(), "/model --naming 1"); err != nil {
		t.Fatalf("naming model switch: %v", err)
	}
	if !strings.Contains(p.out.String(), "switched naming model: deepseek/deepseek-chat") {
		t.Fatalf("unexpected naming switch output: %q", p.out.String())
	}
	data, err = os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read naming state: %v", err)
	}
	state = string(data)
	if !strings.Contains(state, `[naming_model]`) || !strings.Contains(state, `model = 'deepseek-chat'`) {
		t.Fatalf("naming state not persisted: %s", state)
	}
	p.out.Reset()
	if err := a.HandleMessage(context.Background(), "/models deepseek"); err != nil {
		t.Fatalf("models after naming: %v", err)
	}
	if !strings.Contains(p.out.String(), "naming") {
		t.Fatalf("models missing naming marker: %q", p.out.String())
	}
}

func TestModelsShowsMissingAPIKeyEnv(t *testing.T) {
	p := &fakePlatform{}
	providers := map[string]config.ProviderConfig{
		"local":  {Models: []string{"local-model"}},
		"openai": {BaseURL: "https://example.invalid/v1", APIKeyEnv: "OPENAI_API_KEY", Models: []string{"configured-openai"}},
	}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "local", Model: "local-model"},
		storage.SessionModeChat: {Provider: "local", Model: "local-model"},
	}
	a := NewWithOptions(Options{Platform: p, Client: &fakeLLM{models: []string{"local-model"}}, ModeModels: modeModels, Providers: providers, Store: newTestStore(t), CommandPrefixes: []string{"/"}, SessionConfig: session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork}})

	if err := a.HandleMessage(context.Background(), "/models"); err != nil {
		t.Fatalf("models: %v", err)
	}
	got := p.out.String()
	for _, want := range []string{"local-model", "configured-openai", "model provider errors:", `openai: api_key_env "OPENAI_API_KEY" is not set`} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestModelsShowsProviderFetchErrorsAndHealthyModels(t *testing.T) {
	p := &fakePlatform{}
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"ok-remote"}]}`)
	}))
	defer okSrv.Close()
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"bad api key"}}`, http.StatusUnauthorized)
	}))
	defer badSrv.Close()

	providers := map[string]config.ProviderConfig{
		"bad":   {BaseURL: badSrv.URL, APIKey: "wrong-key", Models: []string{"bad-configured"}},
		"local": {Models: []string{"local-model"}},
		"ok":    {BaseURL: okSrv.URL, APIKey: "ok-key", Models: []string{"ok-configured"}},
	}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "local", Model: "local-model"},
		storage.SessionModeChat: {Provider: "local", Model: "local-model"},
	}
	a := NewWithOptions(Options{Platform: p, Client: &fakeLLM{models: []string{"local-model"}}, ModeModels: modeModels, Providers: providers, Store: newTestStore(t), CommandPrefixes: []string{"/"}, SessionConfig: session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork}})

	if err := a.HandleMessage(context.Background(), "/models"); err != nil {
		t.Fatalf("models: %v", err)
	}
	got := p.out.String()
	for _, want := range []string{"ok:\n", "ok-configured", "ok-remote", "bad:\n", "bad-configured", "model provider errors:", "bad:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(strings.ToLower(got), "bad api key") && !strings.Contains(got, "401") {
		t.Fatalf("output missing provider error detail:\n%s", got)
	}
}

func TestModelOptionsFetchesProvidersInParallel(t *testing.T) {
	newModelServer := func(model string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(150 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"data":[{"id":%q}]}`, model)
		}))
	}
	srvA := newModelServer("remote-a")
	defer srvA.Close()
	srvB := newModelServer("remote-b")
	defer srvB.Close()

	providers := map[string]config.ProviderConfig{
		"local": {Models: []string{"local-model"}},
		"a":     {BaseURL: srvA.URL, APIKey: "test-key", Models: []string{"configured-a"}},
		"b":     {BaseURL: srvB.URL, APIKey: "test-key", Models: []string{"configured-b"}},
	}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "local", Model: "local-model"},
		storage.SessionModeChat: {Provider: "local", Model: "local-model"},
	}
	a := NewWithOptions(Options{Platform: &fakePlatform{}, Client: &fakeLLM{models: []string{"local-model"}}, ModeModels: modeModels, Providers: providers, Store: newTestStore(t), CommandPrefixes: []string{"/"}, SessionConfig: session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork}})

	startedAt := time.Now()
	options := a.Models("")
	elapsed := time.Since(startedAt)

	if elapsed >= 250*time.Millisecond {
		t.Fatalf("model provider fetches appear serial, elapsed=%s", elapsed)
	}
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
	a := NewWithOptions(Options{Platform: &fakePlatform{}, Client: &fakeLLM{models: []string{"local-model"}}, ModeModels: modeModels, Providers: providers, Store: newTestStore(t), CommandPrefixes: []string{"/"}, SessionConfig: session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork}})

	first := a.ModelList("", agentcommands.ModelListOptions{})
	second := a.ModelList("", agentcommands.ModelListOptions{})
	fresh := a.ModelList("", agentcommands.ModelListOptions{Fresh: true})

	if requests != 2 {
		t.Fatalf("provider model requests = %d, want 2", requests)
	}
	if first.Options[0].Model != "local-model" || second.Options[0].Model != "local-model" || fresh.Options[0].Model != "local-model" {
		t.Fatalf("unexpected cached models: first=%#v second=%#v fresh=%#v", first.Options, second.Options, fresh.Options)
	}
}

func TestModelSwitchUsesMessagePlatformCurrentModeForGlobalState(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	statePath := filepath.Join(t.TempDir(), "state.toml")
	providers := map[string]config.ProviderConfig{
		"deepseek": {Models: []string{"deepseek-chat"}},
		"zhipu":    {Models: []string{"glm-4-flash"}},
	}
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "deepseek", Model: "deepseek-chat"},
		storage.SessionModeChat: {Provider: "deepseek", Model: "deepseek-chat"},
	}
	a := NewWithOptions(Options{Platform: p, Client: &fakeLLM{models: []string{"deepseek-chat"}}, ModeModels: modeModels, Providers: providers, StatePath: statePath, Store: store, CommandPrefixes: []string{"/"}, SessionConfig: session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork}})
	a.RegisterPlatformSender("qq", p)
	qqCtx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qq", PlatformUserID: "admin", ScopeID: "group:9"})
	a.SetSecurityPolicy(security.NewPolicy("low", "high", map[string][]string{"qq": {"admin"}}))

	qqSession, err := a.sessions.Create(qqCtx, a.scope(qqCtx), session.CreateRequest{Title: "qq chat"})
	if err != nil {
		t.Fatalf("create qq session: %v", err)
	}
	qqSession.Mode = storage.SessionModeChat
	if err := store.Sessions().Update(qqCtx, qqSession); err != nil {
		t.Fatalf("update qq session mode: %v", err)
	}

	if err := a.HandleMessage(qqCtx, "/model zhipu/glm-4-flash"); err != nil {
		t.Fatalf("model switch: %v", err)
	}
	if !strings.Contains(p.out.String(), "switched to model: zhipu/glm-4-flash") {
		t.Fatalf("unexpected switch output: %q", p.out.String())
	}
	if got := a.CurrentModelForMode(storage.SessionModeChat); got.Provider != "zhipu" || got.Model != "glm-4-flash" {
		t.Fatalf("chat model = %#v", got)
	}
	if got := a.CurrentModelForMode(storage.SessionModeWork); got.Provider != "deepseek" || got.Model != "deepseek-chat" {
		t.Fatalf("work model = %#v", got)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	state := string(data)
	if !strings.Contains(state, `[mode_models.chat]`) || !strings.Contains(state, `provider = 'zhipu'`) || !strings.Contains(state, `model = 'glm-4-flash'`) {
		t.Fatalf("chat model not persisted globally: %s", state)
	}
	if !strings.Contains(state, `[mode_models.work]`) || !strings.Contains(state, `provider = 'deepseek'`) || !strings.Contains(state, `model = 'deepseek-chat'`) {
		t.Fatalf("work model should stay unchanged: %s", state)
	}
}
