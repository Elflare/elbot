package agent

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	agentcommands "elbot/internal/agent/commands"
	"elbot/internal/config"
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
	selected := a.CurrentModelForMode(a.currentMode())
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

func (a *Agent) currentMode() string {
	current, err := a.sessions.Current(context.Background(), a.scope(context.Background()))
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

func (a *Agent) SelectModel(arg string) (agentcommands.ModelOption, error) {
	return a.SelectModelForMode(a.currentMode(), arg)
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
				Compact:     provider == compact.Provider && model == compact.Model,
				Naming:      provider == naming.Provider && model == naming.Model,
			}
			option.Current = option.ChatCurrent || option.WorkCurrent
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

func (a *Agent) clientForProvider(providerName string) llm.LLM {
	a.modelRuntime.clientsMu.Lock()
	defer a.modelRuntime.clientsMu.Unlock()
	if client := a.modelRuntime.clients[providerName]; client != nil {
		return client
	}
	provider := a.modelRuntime.providers[providerName]
	client := openai.NewWithOptions(provider.BaseURL, provider.APIKey, provider.ExtraPayload, agentModelExtraPayloads(provider.ModelConfigs), llmRequestOptions(a.modelRuntime.llmRequestConfig))

	client.SetLogger(a.logger)
	a.modelRuntime.clients[providerName] = client
	return client
}

func (a *Agent) saveRuntimeState() error {
	return config.SaveState(a.statePath, config.StateConfig{
		Session:      config.StateSessionConfig{DefaultMode: a.sessions.DefaultMode()},
		ModeModels:   cloneModeModels(a.modelRuntime.modeModels),
		CompactModel: a.compactModel,
		NamingModel:  a.namingModel,
	})
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
