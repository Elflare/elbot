package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	agentcommands "elbot/internal/agent/commands"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/llm/openai"
	"elbot/internal/storage"
)

type modelRuntimeState struct {
	llmClient        llm.LLM
	model            string
	providerName     string
	provider         config.ProviderConfig
	llmRequestConfig config.LLMRequestConfig
	providers        map[string]config.ProviderConfig
	modeModels       map[string]config.ModelSelection
	clients          map[string]llm.LLM
	clientsMu        sync.Mutex
	allModels        []string
	modelListCache   map[string]modelListCacheEntry
	modelListMu      sync.Mutex
}

type modelListCacheEntry struct {
	models []string
	err    error
}

type modelListOptions struct {
	Fresh bool
}

func newModelRuntimeState(client llm.LLM, model, providerName string, provider config.ProviderConfig, llmRequestConfig config.LLMRequestConfig, providers map[string]config.ProviderConfig, modeModels map[string]config.ModelSelection, clients map[string]llm.LLM) modelRuntimeState {
	return modelRuntimeState{
		llmClient:        client,
		model:            model,
		providerName:     providerName,
		provider:         provider,
		llmRequestConfig: llmRequestConfig,
		providers:        providers,
		modeModels:       cloneModeModels(modeModels),
		clients:          clients,
		modelListCache:   map[string]modelListCacheEntry{},
	}
}

func (a *Agent) CurrentModel() string {
	return a.CurrentModeModel().Model
}

func (a *Agent) CurrentProvider() string {
	return a.CurrentModeModel().Provider
}

func (a *Agent) CurrentModeModel() agentcommands.ModelOption {
	selected := a.CurrentModelForMode(a.currentMode(context.Background()))
	selected.Current = true
	return selected
}

func (a *Agent) CurrentModelForMode(mode string) agentcommands.ModelOption {
	selected := a.modelForMode(mode)
	return agentcommands.ModelOption{Provider: selected.Provider, Model: selected.Model}
}

func (a *Agent) CurrentCompactModel() agentcommands.ModelOption {
	current, _ := a.sessions.Current(context.Background(), a.scope(context.Background()))
	selected := a.compactSelectionForSession(current)
	return agentcommands.ModelOption{Provider: selected.Provider, Model: selected.Model, Compact: true}
}

func (a *Agent) currentMode(ctx context.Context) string {
	current, err := a.sessions.Current(ctx, a.scope(ctx))
	if err != nil || current.Mode == "" {
		return a.sessions.DefaultMode()
	}
	return current.Mode
}

func (a *Agent) CurrentNamingModel() agentcommands.ModelOption {
	selected := a.namingModel
	if selected.Provider == "" || selected.Model == "" {
		return agentcommands.ModelOption{}
	}
	return agentcommands.ModelOption{Provider: selected.Provider, Model: selected.Model, Naming: true}
}

func (a *Agent) SelectCompactModel(arg string) (agentcommands.ModelOption, error) {
	selected, err := a.selectModelOption(arg)
	if err != nil {
		return agentcommands.ModelOption{}, err
	}
	a.compactModel = config.ModelSelection{Provider: selected.Provider, Model: selected.Model}
	selected.Compact = true
	if a.statePath != "" {
		if err := a.saveRuntimeState(); err != nil {
			return agentcommands.ModelOption{}, err
		}
	}
	return selected, nil
}

func (a *Agent) SelectNamingModel(arg string) (agentcommands.ModelOption, error) {
	selected, err := a.selectModelOption(arg)
	if err != nil {
		return agentcommands.ModelOption{}, err
	}
	a.namingModel = config.ModelSelection{Provider: selected.Provider, Model: selected.Model}
	selected.Naming = true
	if a.titleGen != nil {
		a.titleGen.naming = a.clientForProvider(selected.Provider)
		a.titleGen.namingModel = selected.Model
	}
	if a.statePath != "" {
		if err := a.saveRuntimeState(); err != nil {
			return agentcommands.ModelOption{}, err
		}
	}
	return selected, nil
}

func (a *Agent) SelectModel(ctx context.Context, arg string) (agentcommands.ModelOption, error) {
	return a.SelectModelForMode(a.currentMode(ctx), arg)
}

func (a *Agent) SelectModelForMode(mode, arg string) (agentcommands.ModelOption, error) {
	selected, err := a.selectModelOption(arg)
	if err != nil {
		return agentcommands.ModelOption{}, err
	}

	if err := a.applyModeModelSelection(mode, selected.Provider, selected.Model); err != nil {
		return agentcommands.ModelOption{}, err
	}
	selected.Current = true
	if mode == storage.SessionModeChat {
		selected.ChatCurrent = true
	}
	if mode == storage.SessionModeWork {
		selected.WorkCurrent = true
	}
	if a.statePath != "" {
		if err := a.saveRuntimeState(); err != nil {
			return agentcommands.ModelOption{}, err
		}
	}
	return selected, nil
}

func (a *Agent) selectModelOption(arg string) (agentcommands.ModelOption, error) {
	name := strings.TrimSpace(arg)
	if name == "" {
		return agentcommands.ModelOption{}, fmt.Errorf("usage: /model <name or number>")
	}

	models := a.modelOptions("", modelListOptions{}).Options
	if len(models) == 0 {
		return agentcommands.ModelOption{}, fmt.Errorf("no models available")
	}

	var selected agentcommands.ModelOption
	if idx, err := strconv.Atoi(name); err == nil {
		if idx < 1 || idx > len(models) {
			return agentcommands.ModelOption{}, fmt.Errorf("model index %d out of range [1-%d]", idx, len(models))
		}
		selected = models[idx-1]
	} else {
		matches := []agentcommands.ModelOption{}
		for _, m := range models {
			if strings.EqualFold(m.Model, name) || strings.EqualFold(m.Provider+"/"+m.Model, name) {
				matches = append(matches, m)
			}
		}
		if len(matches) == 0 {
			query := strings.ToLower(name)
			for _, m := range models {
				if strings.Contains(strings.ToLower(m.Model), query) || strings.Contains(strings.ToLower(m.Provider), query) {
					matches = append(matches, m)
				}
			}
		}

		if len(matches) == 1 {
			selected = matches[0]
		} else if len(matches) > 1 {
			return agentcommands.ModelOption{}, fmt.Errorf("ambiguous model %q, matches: %s", name, joinModelMatches(matches))
		} else {
			return agentcommands.ModelOption{}, fmt.Errorf("model %q not found", name)
		}
	}
	return selected, nil
}

func (a *Agent) Models(query string) []agentcommands.ModelOption {
	return a.ModelList(query, agentcommands.ModelListOptions{}).Options
}

func (a *Agent) ModelList(query string, opts agentcommands.ModelListOptions) agentcommands.ModelListResult {
	return a.modelOptions(query, modelListOptions{Fresh: opts.Fresh})
}

func (a *Agent) modelOptions(query string, opts modelListOptions) agentcommands.ModelListResult {
	query = strings.ToLower(strings.TrimSpace(query))
	providers := make([]string, 0, len(a.modelRuntime.providers))
	for provider := range a.modelRuntime.providers {
		providers = append(providers, provider)
	}
	sort.Strings(providers)

	modelsByProvider := make([][]string, len(providers))
	errorsByProvider := make([]error, len(providers))
	var wg sync.WaitGroup
	wg.Add(len(providers))
	for i, provider := range providers {
		i, provider := i, provider
		go func() {
			defer wg.Done()
			modelsByProvider[i], errorsByProvider[i] = a.cachedProviderModels(provider, opts.Fresh)
		}()
	}
	wg.Wait()

	chat := a.modelForMode(storage.SessionModeChat)
	work := a.modelForMode(storage.SessionModeWork)
	modeMarks := a.modelModeMarks([]string{storage.SessionModeChat, storage.SessionModeWork, "elwisp1", "elwisp2", "elwisp3"})
	compact := a.compactSelectionForSession(nil)
	naming := a.namingModel
	options := []agentcommands.ModelOption{}
	idx := 1
	for i, provider := range providers {
		for _, model := range modelsByProvider[i] {
			option := agentcommands.ModelOption{
				Index:       idx,
				Provider:    provider,
				Model:       model,
				ChatCurrent: provider == chat.Provider && model == chat.Model,
				WorkCurrent: provider == work.Provider && model == work.Model,
				ModeMarks:   modeMarks[modelSelectionKey(provider, model)],
				Compact:     provider == compact.Provider && model == compact.Model,
				Naming:      provider == naming.Provider && model == naming.Model,
			}
			option.Current = len(option.ModeMarks) > 0
			idx++
			if query != "" && !strings.Contains(strings.ToLower(provider), query) && !strings.Contains(strings.ToLower(model), query) {
				continue
			}
			options = append(options, option)
		}
	}
	a.modelRuntime.allModels = make([]string, 0, len(options))
	for _, option := range options {
		a.modelRuntime.allModels = append(a.modelRuntime.allModels, option.Model)
	}
	errors := []agentcommands.ModelProviderError{}
	for i, provider := range providers {
		if errorsByProvider[i] != nil {
			errors = append(errors, agentcommands.ModelProviderError{Provider: provider, Err: errorsByProvider[i]})
		}
	}
	return agentcommands.ModelListResult{Options: options, Errors: errors}
}

func (a *Agent) cachedProviderModels(providerName string, fresh bool) ([]string, error) {
	if !fresh {
		a.modelRuntime.modelListMu.Lock()
		entry, ok := a.modelRuntime.modelListCache[providerName]
		a.modelRuntime.modelListMu.Unlock()
		if ok {
			return append([]string(nil), entry.models...), entry.err
		}
	}
	models, err := a.sortedProviderModels(providerName)
	a.modelRuntime.modelListMu.Lock()
	a.modelRuntime.modelListCache[providerName] = modelListCacheEntry{models: append([]string(nil), models...), err: err}
	a.modelRuntime.modelListMu.Unlock()
	return models, err
}

func (a *Agent) sortedProviderModels(providerName string) ([]string, error) {
	provider, ok := a.modelRuntime.providers[providerName]
	if !ok {
		return nil, nil
	}
	set := map[string]struct{}{}

	client := a.clientForProvider(providerName)
	fetchFromAPI := providerName == a.modelRuntime.providerName || (provider.BaseURL != "" && provider.APIKey != "")
	var fetchErr error
	if strings.TrimSpace(provider.APIKey) == "" && strings.TrimSpace(provider.APIKeyEnv) != "" && strings.TrimSpace(provider.BaseURL) != "" {
		fetchErr = fmt.Errorf("api_key_env %q is not set", provider.APIKeyEnv)
	} else if fetchFromAPI {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		fetched, err := client.ListModels(ctx)
		if err != nil {
			fetchErr = err
		} else {
			for _, m := range fetched {
				set[m] = struct{}{}
			}
		}
	}
	for _, m := range provider.Models {
		set[m] = struct{}{}
	}

	models := make([]string, 0, len(set))
	for m := range set {
		models = append(models, m)
	}
	sort.Strings(models)
	return models, fetchErr
}

func (a *Agent) applyModeModelSelection(mode, providerName, model string) error {
	provider, ok := a.modelRuntime.providers[providerName]
	if !ok {
		return fmt.Errorf("provider %q not found", providerName)
	}
	a.modelRuntime.modeModels[mode] = config.ModelSelection{Provider: providerName, Model: model}
	a.modelRuntime.providerName = providerName
	a.modelRuntime.provider = provider
	a.modelRuntime.model = model
	a.modelRuntime.llmClient = a.clientForProvider(providerName)
	if a.titleGen != nil && mode == storage.SessionModeWork {
		a.titleGen.primary = a.modelRuntime.llmClient
		a.titleGen.primaryModel = model
	}
	return nil
}

func (a *Agent) modelForMode(mode string) config.ModelSelection {
	selected := a.modelRuntime.modeModels[mode]
	if selected.Provider == "" || selected.Model == "" {
		return a.modelRuntime.modeModels[storage.SessionModeWork]
	}
	return selected
}

func (a *Agent) modelModeMarks(modes []string) map[string][]string {
	marks := map[string][]string{}
	seen := map[string]bool{}
	for _, mode := range modes {
		selected := a.modelForMode(mode)
		if selected.Provider == "" || selected.Model == "" {
			continue
		}
		key := modelSelectionKey(selected.Provider, selected.Model)
		markKey := key + "\x00" + mode
		if seen[markKey] {
			continue
		}
		seen[markKey] = true
		marks[key] = append(marks[key], mode)
	}
	return marks
}

func modelSelectionKey(provider, model string) string {
	return provider + "\x00" + model
}

func (a *Agent) clientForProvider(providerName string) llm.LLM {
	a.modelRuntime.clientsMu.Lock()
	defer a.modelRuntime.clientsMu.Unlock()
	if client := a.modelRuntime.clients[providerName]; client != nil {
		return client
	}
	provider := a.modelRuntime.providers[providerName]
	client := openai.NewWithOptions(provider.BaseURL, provider.APIKey, provider.ExtraPayload, agentModelExtraPayloads(provider.ModelConfigs), llmRequestOptions(a.modelRuntime.llmRequestConfig))

	client.SetLogger(a.logger)
	a.attachLLMRetryNotifier(client, providerName)
	a.modelRuntime.clients[providerName] = client
	return client
}

func (a *Agent) attachLLMRetryNotifier(client llm.LLM, providerName string) {
	adapter, ok := client.(*openai.Adapter)
	if !ok {
		return
	}
	adapter.SetRetryNotifier(func(ctx context.Context, event openai.RetryEvent) {
		if event.Err == nil || errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return
		}
		text := fmt.Sprintf("LLM 请求失败，正在重试 %d/%d（%s 后）：%v", event.Attempt, event.MaxRetries, event.Delay.Round(time.Millisecond), event.Err)
		if providerName != "" {
			text = fmt.Sprintf("LLM 请求失败，正在重试 %d/%d（provider=%s，%s 后）：%v", event.Attempt, event.MaxRetries, providerName, event.Delay.Round(time.Millisecond), event.Err)
		}
		_, _ = a.SendNoticeOutput(ctx, delivery.Target{}, delivery.Text(text))
	})
}

func initialStateModTime(path string) time.Time {
	if path == "" {
		return time.Time{}
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func (a *Agent) refreshRuntimeState() {
	if a.statePath == "" {
		return
	}
	info, err := os.Stat(a.statePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) && a.logger != nil {
			a.logger.Warn("check state config", "path", a.statePath, "error", err.Error())
		}
		return
	}
	modTime := info.ModTime()
	if !a.stateModTime.IsZero() && !modTime.After(a.stateModTime) {
		return
	}
	state, err := config.LoadState(a.statePath)
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("load state config", "path", a.statePath, "error", err.Error())
		}
		return
	}
	if err := a.applyRuntimeState(state); err != nil {
		if a.logger != nil {
			a.logger.Warn("apply state config", "path", a.statePath, "error", err.Error())
		}
		return
	}
	a.stateModTime = modTime
}

func (a *Agent) applyRuntimeState(state *config.StateConfig) error {
	for _, selection := range state.ModeModels {
		if selection.Provider == "" || selection.Model == "" {
			continue
		}
		if _, ok := a.modelRuntime.providers[selection.Provider]; !ok {
			return fmt.Errorf("provider %q not found", selection.Provider)
		}
	}
	if state.CompactModel.Provider != "" && state.CompactModel.Model != "" {
		if _, ok := a.modelRuntime.providers[state.CompactModel.Provider]; !ok {
			return fmt.Errorf("provider %q not found", state.CompactModel.Provider)
		}
	}
	if state.NamingModel.Provider != "" && state.NamingModel.Model != "" {
		if _, ok := a.modelRuntime.providers[state.NamingModel.Provider]; !ok {
			return fmt.Errorf("provider %q not found", state.NamingModel.Provider)
		}
	}
	for mode, selection := range state.ModeModels {
		if selection.Provider == "" || selection.Model == "" {
			continue
		}
		if err := a.applyModeModelSelection(mode, selection.Provider, selection.Model); err != nil {
			return err
		}
	}
	if state.CompactModel.Provider != "" && state.CompactModel.Model != "" {
		a.compactModel = state.CompactModel
	}
	if state.NamingModel.Provider != "" && state.NamingModel.Model != "" {
		a.namingModel = state.NamingModel
		if a.titleGen != nil {
			a.titleGen.naming = a.clientForProvider(state.NamingModel.Provider)
			a.titleGen.namingModel = state.NamingModel.Model
		}
	}
	return nil
}

func (a *Agent) saveRuntimeState() error {
	if err := config.SaveState(a.statePath, config.StateConfig{
		Session:      config.StateSessionConfig{DefaultMode: a.sessions.DefaultMode()},
		ModeModels:   cloneModeModels(a.modelRuntime.modeModels),
		CompactModel: a.compactModel,
		NamingModel:  a.namingModel,
	}); err != nil {
		return err
	}
	a.stateModTime = initialStateModTime(a.statePath)
	return nil
}

func joinModelMatches(matches []agentcommands.ModelOption) string {
	parts := make([]string, len(matches))
	for i, m := range matches {
		parts[i] = fmt.Sprintf("[%d] %s/%s", m.Index, m.Provider, m.Model)
	}
	return strings.Join(parts, ", ")
}

func llmRequestOptions(cfg config.LLMRequestConfig) openai.RequestOptions {
	return openai.RequestOptions{
		Timeout:           time.Duration(cfg.TimeoutSeconds) * time.Second,
		MaxRetries:        cfg.MaxRetries,
		RetryInitialDelay: time.Duration(cfg.RetryInitialDelaySeconds) * time.Second,
	}
}

func agentModelExtraPayloads(modelConfigs map[string]config.ModelConfig) map[string]map[string]any {
	out := map[string]map[string]any{}

	for model, cfg := range modelConfigs {
		if cfg.ExtraPayload != nil {
			out[model] = cfg.ExtraPayload
		}
	}
	return out
}
