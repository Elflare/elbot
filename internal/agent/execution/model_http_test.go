package execution

import (
	"context"
	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	"encoding/json"
	"fmt"
	"github.com/pelletier/go-toml/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// These fixtures serve real HTTP; no test factory is exposed by modelmgr.
type testModelOptions struct {
	Clients                   map[string]llm.Client
	Providers                 map[string]config.ProviderConfig
	ModeModels                map[string]config.ModelSelection
	CompactModel, NamingModel config.ModelSelection
	DefaultMode               string
	Metadata                  config.ModelMetadataConfig
}

func testModelConfig(t *testing.T, opts testModelOptions) *config.Config {
	t.Helper()
	// 夹具绕过配置加载，显式补齐配置层提供的窗口默认值。
	if opts.Metadata.DefaultContextWindow == 0 {
		opts.Metadata.DefaultContextWindow = config.DefaultContextWindow
	}
	cfg := &config.Config{Providers: make(map[string]config.ProviderConfig), ModeModels: opts.ModeModels, CompactModel: opts.CompactModel, NamingModel: opts.NamingModel, Session: config.SessionConfig{DefaultMode: opts.DefaultMode}, ModelMetadata: opts.Metadata}
	dir := t.TempDir()
	cfg.ConfigPath = filepath.Join(dir, "app.toml")
	cfg.ProvidersConfigPath = filepath.Join(dir, "providers.toml")
	for name, p := range opts.Providers {
		if p.BaseURL == "" {
			client := opts.Clients[name]
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					ids := p.Models
					if client != nil {
						var err error
						ids, err = client.ListModels(r.Context())
						if err != nil {
							http.Error(w, err.Error(), 400)
							return
						}
					}
					data := []map[string]string{}
					for _, id := range ids {
						data = append(data, map[string]string{"id": id})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
					return
				}
				streamer, ok := client.(interface {
					Stream(context.Context, chatcompletions.Request) (<-chan chatcompletions.Chunk, error)
				})
				if !ok {
					http.Error(w, "unexpected model call", 400)
					return
				}
				req, err := decodeTestChat(r)
				if err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				chunks, err := streamer.Stream(r.Context(), req)
				if err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				for {
					select {
					case <-r.Context().Done():
						return
					case chunk, ok := <-chunks:
						if !ok {
							fmt.Fprint(w, "data: [DONE]\n\n")
							return
						}
						if chunk.Error != nil {
							fmt.Fprint(w, "data: invalid-json\n\n")
							w.(http.Flusher).Flush()
							return
						}
						delta := map[string]any{"content": chunk.DeltaContent, "reasoning_content": chunk.DeltaReasoningContent}
						finish := chunk.FinishReason
						if len(chunk.ToolCallDeltas) > 0 {
							calls := []map[string]any{}
							for _, call := range chunk.ToolCallDeltas {
								calls = append(calls, map[string]any{"index": call.Index, "id": call.ID, "function": map[string]string{"name": call.Name, "arguments": call.Args}})
							}
							delta["tool_calls"] = calls
							finish = "tool_calls"
						}
						choice := map[string]any{"index": 0, "delta": delta}
						if finish != "" {
							choice["finish_reason"] = finish
						}
						body := map[string]any{"choices": []any{choice}}
						if u := chunk.Usage; u != nil {
							body["usage"] = map[string]any{"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens, "total_tokens": u.TotalTokens, "prompt_cache_hit_tokens": u.CacheHitTokens}
						}
						data, err := json.Marshal(body)
						if err != nil {
							t.Error(err)
							return
						}
						fmt.Fprintf(w, "data: %s\n\n", data)
						w.(http.Flusher).Flush()
					}
				}
			}))
			t.Cleanup(server.Close)
			p.BaseURL = server.URL
		}
		cfg.Providers[name] = p
	}
	writeTestProviders(t, cfg)
	return cfg
}
func writeTestProviders(t *testing.T, cfg *config.Config) {
	t.Helper()
	data, err := toml.Marshal(config.ProviderFile{Providers: cfg.Providers, ModelMetadata: cfg.ModelMetadata})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.ProvidersConfigPath, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func newConfiguredTestModels(t *testing.T, opts testModelOptions) (*modelmgr.ModelManager, error) {
	t.Helper()
	m, err := modelmgr.NewModelManager(t.Context(), testModelConfig(t, opts))
	if m != nil {
		t.Cleanup(func() {
			if err := m.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	return m, err
}

func decodeTestChat(r *http.Request) (chatcompletions.Request, error) {
	var raw struct {
		Model       string
		Temperature float64
		MaxTokens   int `json:"max_tokens"`
		Messages    []struct {
			Role       llm.MessageRole
			Content    json.RawMessage
			Name       string
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string
				Function struct{ Name, Arguments string }
			} `json:"tool_calls"`
		}
		Tools []struct{ Function llm.ToolSchema }
	}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return chatcompletions.Request{}, err
	}
	req := chatcompletions.Request{Model: raw.Model, Temperature: raw.Temperature, MaxTokens: raw.MaxTokens}
	for _, m := range raw.Messages {
		msg := llm.LLMMessage{Role: m.Role, Name: m.Name, ToolCallID: m.ToolCallID}
		var text string
		if json.Unmarshal(m.Content, &text) == nil {
			msg.Segments = llm.TextSegments(text)
		} else {
			var parts []struct {
				Type, Text string
				ImageURL   struct{ URL string } `json:"image_url"`
			}
			if err := json.Unmarshal(m.Content, &parts); err != nil {
				return req, err
			}
			for _, part := range parts {
				if part.Type == "image_url" {
					msg.Segments = append(msg.Segments, llm.MessageSegment{Type: llm.SegmentImage, URL: part.ImageURL.URL})
				} else {
					msg.Segments = append(msg.Segments, llm.MessageSegment{Type: llm.SegmentText, Text: part.Text})
				}
			}
		}
		for _, call := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, llm.ToolCallRequest{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
		}
		req.Messages = append(req.Messages, msg)
	}
	for _, tool := range raw.Tools {
		req.Tools = append(req.Tools, tool.Function)
	}
	return req, nil
}
