package agent

import (
	"context"
	"sync"

	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type contextRuntimeState struct {
	store          storage.Store
	sessions       *session.Service
	requests       *request.Manager
	turns          *turn.Manager
	loader         contextmgr.Loader
	windowResolver *contextmgr.WindowResolver
	compressor     contextmgr.Compressor

	mu            sync.Mutex
	config        config.ContextConfig
	modelMetadata config.ModelMetadataConfig
	compactModel  config.ModelSelection
	lastUsage     map[string]*llm.Usage
}

func newContextRuntimeState(store storage.Store, sessions *session.Service, requests *request.Manager, turns *turn.Manager) contextRuntimeState {
	return contextRuntimeState{
		store:     store,
		sessions:  sessions,
		requests:  requests,
		turns:     turns,
		loader:    contextmgr.Loader{Store: store},
		config:    config.Default().Context,
		lastUsage: map[string]*llm.Usage{},
	}
}

func (r *contextRuntimeState) configure(ctxCfg config.ContextConfig, metadata config.ModelMetadataConfig, providers map[string]config.ProviderConfig, compactModel config.ModelSelection, clientFor contextmgr.ClientProvider) {
	r.mu.Lock()
	r.config = ctxCfg
	r.modelMetadata = metadata
	r.compactModel = compactModel
	r.loader = contextmgr.Loader{Store: r.store}
	r.windowResolver = contextmgr.NewWindowResolver(metadata, providers, clientFor)
	r.compressor = contextmgr.Compressor{ClientFor: clientFor}
	r.mu.Unlock()
}

func (r *contextRuntimeState) load(ctx context.Context, sessionID string) (*contextmgr.LoadedContext, error) {
	r.mu.Lock()
	loader := r.loader
	r.mu.Unlock()
	return loader.Load(ctx, sessionID)
}

func (r *contextRuntimeState) loadRawMessages(ctx context.Context, sessionID string) ([]storage.Message, error) {
	r.mu.Lock()
	loader := r.loader
	r.mu.Unlock()
	return loader.LoadRawMessages(ctx, sessionID)
}

func (r *contextRuntimeState) compactSelection(fallback config.ModelSelection) config.ModelSelection {
	r.mu.Lock()
	selected := r.compactModel
	r.mu.Unlock()
	if selected.Provider != "" && selected.Model != "" {
		return selected
	}
	return fallback
}

func (r *contextRuntimeState) configuredCompactModel() config.ModelSelection {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.compactModel
}

func (r *contextRuntimeState) setCompactModel(selection config.ModelSelection) {
	r.mu.Lock()
	r.compactModel = selection
	r.mu.Unlock()
}

func (a *Agent) SetContextOptions(ctxCfg config.ContextConfig, metadata config.ModelMetadataConfig, providers map[string]config.ProviderConfig, compactModel config.ModelSelection) {
	a.contextRuntime.configure(ctxCfg, metadata, providers, compactModel, a.clientForProvider)
}

func (a *Agent) compactSelectionForSession(session *storage.Session) config.ModelSelection {
	mode := storage.SessionModeWork
	if session != nil && session.Mode != "" {
		mode = session.Mode
	}
	return a.contextRuntime.compactSelection(a.modelForMode(mode))
}
