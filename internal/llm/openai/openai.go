package openai

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"elbot/internal/llm"
)

const (
	defaultFirstChunkTimeout = 180 * time.Second
	defaultStreamIdleTimeout = 60 * time.Second
	defaultMaxRetries        = 3
	defaultRetryDelay        = 2 * time.Second
)

type RetryEvent struct {
	Attempt    int
	MaxRetries int
	Delay      time.Duration
	Err        error
}

type RequestOptions struct {
	FirstChunkTimeout time.Duration
	StreamIdleTimeout time.Duration
	MaxRetries        int
	RetryInitialDelay time.Duration
	OnRetry           func(context.Context, RetryEvent)
	Proxy             string
}

func (o RequestOptions) withDefaults() RequestOptions {
	if o.FirstChunkTimeout <= 0 {
		o.FirstChunkTimeout = defaultFirstChunkTimeout
	}
	if o.StreamIdleTimeout <= 0 {
		o.StreamIdleTimeout = defaultStreamIdleTimeout
	}
	if o.MaxRetries <= 0 {
		o.MaxRetries = defaultMaxRetries
	}
	if o.RetryInitialDelay <= 0 {
		o.RetryInitialDelay = defaultRetryDelay
	}
	return o
}

// Adapter implements llm.LLM for OpenAI-compatible APIs.
type Adapter struct {
	baseURL            string
	apiKey             string
	extraPayload       map[string]any
	modelExtraPayloads map[string]map[string]any
	client             *http.Client
	firstChunkTimeout  time.Duration
	streamIdleTimeout  time.Duration
	maxRetries         int
	retryInitialDelay  time.Duration
	onRetry            func(context.Context, RetryEvent)

	logger         *slog.Logger
	loggedSystemMu sync.Mutex
	loggedSystem   map[string]bool
}

// New creates a new OpenAI-compatible adapter.
func New(baseURL, apiKey string, extraPayload map[string]any) *Adapter {
	return NewWithModelExtraPayloads(baseURL, apiKey, extraPayload, nil)
}

func NewWithModelExtraPayloads(baseURL, apiKey string, extraPayload map[string]any, modelExtraPayloads map[string]map[string]any) *Adapter {
	return NewWithOptions(baseURL, apiKey, extraPayload, modelExtraPayloads, RequestOptions{})
}

func NewWithOptions(baseURL, apiKey string, extraPayload map[string]any, modelExtraPayloads map[string]map[string]any, opts RequestOptions) *Adapter {
	baseURL = strings.TrimRight(baseURL, "/")
	opts = opts.withDefaults()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = opts.FirstChunkTimeout
	client := &http.Client{Transport: transport}
	if strings.TrimSpace(opts.Proxy) != "" {
		proxyURL, err := url.Parse(opts.Proxy)
		if err != nil {
			panic(fmt.Sprintf("invalid proxy URL %q: %v", opts.Proxy, err))
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &Adapter{
		baseURL:            baseURL,
		apiKey:             apiKey,
		extraPayload:       extraPayload,
		modelExtraPayloads: modelExtraPayloads,
		client:             client,
		firstChunkTimeout:  opts.FirstChunkTimeout,
		streamIdleTimeout:  opts.StreamIdleTimeout,
		maxRetries:         opts.MaxRetries,
		retryInitialDelay:  opts.RetryInitialDelay,
		onRetry:            opts.OnRetry,
		loggedSystem:       map[string]bool{},
	}
}

func (a *Adapter) SetLogger(logger *slog.Logger) {
	a.logger = logger
}

func (a *Adapter) SetRetryNotifier(onRetry func(context.Context, RetryEvent)) {
	a.onRetry = onRetry
}

func (a *Adapter) endpoint() string {
	return strings.TrimRight(a.baseURL, "/") + "/chat/completions"
}

func (a *Adapter) validateBaseURL() error {
	if strings.TrimSpace(a.baseURL) == "" {
		return errors.New("openai base_url is required")
	}
	return nil
}

// ChatStream sends a chat request and returns a channel of streaming chunks.
func (a *Adapter) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	if err := a.validateBaseURL(); err != nil {
		return nil, err
	}

	body := map[string]any{
		"model":    req.Model,
		"messages": toOpenAIMessages(req.Messages),
		"stream":   true,
		"stream_options": map[string]any{
			"include_usage": true,
		},
	}
	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}
	if len(req.Tools) > 0 {
		body["tools"] = toOpenAITools(req.Tools)
	}

	// Merge provider-level extra payload.
	for k, v := range a.extraPayload {
		body[k] = v
	}

	// Merge model-level extra payload.
	for k, v := range a.modelExtraPayloads[req.Model] {
		body[k] = v
	}

	// Merge request-level extra body (highest priority).
	for k, v := range req.ExtraBody {
		body[k] = v
	}

	bodyBytes, err := marshalJSONNoEscape(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	a.logChatRequest(req, bodyBytes)

	responseCtx, cancel := context.WithCancel(ctx)

	resp, err := a.doWithRetry(responseCtx, func(ctx context.Context) (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint(), bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
		httpReq.Header.Set("Content-Type", "application/json")
		return httpReq, nil
	})
	if err != nil {
		cancel()
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		cancel()
		return nil, parseError(resp)
	}

	ch := make(chan llm.StreamChunk)
	go a.readStream(responseCtx, cancel, resp.Body, ch)
	return ch, nil
}

func (a *Adapter) doWithRetry(ctx context.Context, newRequest func(context.Context) (*http.Request, error)) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= a.maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		req, err := newRequest(ctx)
		if err != nil {
			return nil, err
		}
		resp, err := a.client.Do(req)
		if err == nil && !isRetryableStatus(resp.StatusCode) {
			return resp, nil
		}

		if err != nil {
			lastErr = fmt.Errorf("http request: %w", err)
		} else {
			lastErr = retryableStatusError(resp)
		}

		if attempt == a.maxRetries {
			return nil, lastErr
		}
		delay := retryDelay(a.retryInitialDelay, attempt)
		if a.onRetry != nil {
			a.onRetry(ctx, RetryEvent{Attempt: attempt + 1, MaxRetries: a.maxRetries, Delay: delay, Err: lastErr})
		}
		if err := waitRetryDelay(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func retryDelay(initial time.Duration, attempt int) time.Duration {
	return initial << attempt
}

func waitRetryDelay(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isRetryableStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusConflict ||
		status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests ||
		status >= http.StatusInternalServerError
}

func retryableStatusError(resp *http.Response) error {
	defer resp.Body.Close()
	if err := parseError(resp); err != nil {
		return err
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}

func (a *Adapter) logChatRequest(req llm.ChatRequest, bodyBytes []byte) {
	if a.logger == nil {
		return
	}
	a.logFirstSystemMessage(req)
	attrs := []any{"endpoint", a.endpoint(), "model", req.Model, "session_id", req.SessionID, "latest_message_json", latestMessageJSON(req.Messages)}
	// Debug 日志默认只记录请求摘要，不记录 Authorization 和完整 body。
	// 完整 body 可能包含用户正文、图片 URL、工具参数等敏感信息，
	// 需要临时排查时再手动打开。
	// attrs := []any{"endpoint", a.endpoint(), "model", req.Model, "body_json", string(bodyBytes)}
	attrs = append(attrs, chatRequestLogSummary(req, bodyBytes)...)
	a.logger.Debug("openai chat request", attrs...)
}

func (a *Adapter) logFirstSystemMessage(req llm.ChatRequest) {
	if req.SessionID == "" || firstSystemText(req.Messages) == "" {
		return
	}
	a.loggedSystemMu.Lock()
	if a.loggedSystem[req.SessionID] {
		a.loggedSystemMu.Unlock()
		return
	}
	a.loggedSystem[req.SessionID] = true
	a.loggedSystemMu.Unlock()

	a.logger.Info("system prompt",
		"event", "system_message",
		"session_id", req.SessionID,
		"model", req.Model,
		"first_system_message_json", firstSystemMessageJSON(req.Messages),
	)
}

func latestMessageJSON(messages []llm.LLMMessage) string {
	if len(messages) == 0 {
		return ""
	}
	latest := toOpenAIMessages(messages[len(messages)-1:])
	data, err := marshalJSONNoEscape(latest[0])
	if err != nil {
		return ""
	}
	return string(data)
}

func firstSystemMessageJSON(messages []llm.LLMMessage) string {
	for _, message := range messages {
		if message.Role != llm.RoleSystem {
			continue
		}
		converted := toOpenAIMessages([]llm.LLMMessage{message})
		data, err := marshalJSONNoEscape(converted[0])
		if err != nil {
			return ""
		}
		return string(data)
	}
	return ""
}

func chatRequestLogSummary(req llm.ChatRequest, bodyBytes []byte) []any {
	roles := make([]string, 0, len(req.Messages))
	for _, message := range req.Messages {
		role := string(message.Role)
		if message.Name != "" {
			role += ":" + message.Name
		}
		roles = append(roles, role)
	}
	latest := ""
	if len(roles) > 0 {
		latest = roles[len(roles)-1]
	}
	return []any{
		"message_count", len(req.Messages),
		"message_roles", strings.Join(roles, ","),
		"latest_message", latest,
		"tool_count", len(req.Tools),
		"system_hash", hashText(firstSystemText(req.Messages)),
		"tools_hash", hashJSON(req.Tools),
		"body_hash", hashBytes(bodyBytes),
	}
}

func firstSystemText(messages []llm.LLMMessage) string {
	for _, message := range messages {
		if message.Role == llm.RoleSystem {
			return llm.SegmentsContentText(message.Segments)
		}
	}
	return ""
}

func hashJSON(value any) string {
	data, err := marshalJSONNoEscape(value)
	if err != nil {
		return ""
	}
	return hashBytes(data)
}

func hashText(text string) string {
	return hashBytes([]byte(text))
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func marshalJSONNoEscape(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

// ListModels fetches available model IDs from the /models endpoint.
func (a *Adapter) ListModels(ctx context.Context) ([]string, error) {
	result, err := a.fetchModels(ctx)
	if err != nil {
		return nil, err
	}

	models := make([]string, len(result.Data))
	for i, m := range result.Data {
		models[i] = m.ID
	}
	return models, nil
}

func (a *Adapter) ListModelMetadata(ctx context.Context) ([]llm.ModelMetadata, error) {
	result, err := a.fetchModels(ctx)
	if err != nil {
		return nil, err
	}

	models := make([]llm.ModelMetadata, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, llm.ModelMetadata{ID: m.ID, ContextWindow: m.ContextWindow()})
	}
	return models, nil
}

func (a *Adapter) fetchModels(ctx context.Context) (*openAIModelList, error) {
	if err := a.validateBaseURL(); err != nil {
		return nil, err
	}
	url := strings.TrimRight(a.baseURL, "/") + "/models"

	resp, err := a.doWithRetry(ctx, func(ctx context.Context) (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
		return httpReq, nil
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, parseError(resp)
	}

	var result openAIModelList
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode models response: %w", err)
	}
	return &result, nil
}

// --- request types ---

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content"`
	Name       string           `json:"name,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

type openAIImageURL struct {
	URL string `json:"url"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func toOpenAIMessages(msgs []llm.LLMMessage) []openAIMessage {
	out := make([]openAIMessage, len(msgs))
	for i, m := range msgs {
		out[i] = openAIMessage{
			Role:       string(m.Role),
			Content:    toOpenAIContent(m.Segments),
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
			ToolCalls:  toOpenAIToolCalls(m.ToolCalls),
		}
	}
	return out
}

func toOpenAIContent(segments []llm.MessageSegment) any {
	if len(segments) == 0 {
		return ""
	}
	if len(segments) == 1 && segments[0].Type == llm.SegmentText {
		return segments[0].Text
	}
	parts := make([]openAIContentPart, 0, len(segments))
	for _, segment := range segments {
		switch segment.Type {
		case llm.SegmentText:
			if segment.Text != "" {
				parts = append(parts, openAIContentPart{Type: "text", Text: segment.Text})
			}
		case llm.SegmentImage:
			if segment.URL != "" {
				parts = append(parts, openAIContentPart{Type: "image_url", ImageURL: &openAIImageURL{URL: segment.URL}})
			}
		case llm.SegmentFile:
			// TODO: 后续按厂商能力支持文件输入；当前统一作为文本描述发送。
			if text := llm.SegmentsContentText([]llm.MessageSegment{segment}); text != "" {
				parts = append(parts, openAIContentPart{Type: "text", Text: text})
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return parts
}

func toOpenAIToolCalls(calls []llm.ToolCallRequest) []openAIToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]openAIToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, openAIToolCall{
			ID:   call.ID,
			Type: "function",
			Function: openAIToolFunction{
				Name:      call.Name,
				Arguments: call.Arguments,
			},
		})
	}
	return out
}

func toOpenAITools(tools []llm.ToolSchema) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		toolType := tool.Type
		if toolType == "" {
			toolType = "function"
		}
		out = append(out, map[string]any{
			"type": toolType,
			"function": map[string]any{
				"name":        tool.Function.Name,
				"description": tool.Function.Description,
				"parameters":  tool.Function.Parameters,
			},
		})
	}
	return out
}

// --- response types ---

type openAIStreamChunk struct {
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage"`
}

type openAIChoice struct {
	Index        int         `json:"index"`
	Delta        openAIDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type openAIDelta struct {
	Content          string                `json:"content"`
	ReasoningContent string                `json:"reasoning_content"`
	ToolCalls        []openAIToolCallDelta `json:"tool_calls"`
}

type openAIToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CacheHitTokens   int
}

func (u *openAIUsage) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	u.PromptTokens = firstJSONInt(raw, "prompt_tokens", "input_tokens")
	u.CompletionTokens = firstJSONInt(raw, "completion_tokens", "output_tokens")
	u.TotalTokens = firstJSONInt(raw, "total_tokens")
	u.CacheHitTokens = firstJSONInt(raw,
		"prompt_cache_hit_tokens",
		"cache_hit_tokens",
		"cached_tokens",
		"input_cache_hit_tokens",
		"prompt_cache_read_tokens",
		"cache_read_input_tokens",
	)
	if u.CacheHitTokens == 0 {
		u.CacheHitTokens = firstNestedJSONInt(raw,
			[]string{"prompt_tokens_details", "cached_tokens"},
			[]string{"prompt_tokens_details", "cache_hit_tokens"},
			[]string{"input_tokens_details", "cached_tokens"},
			[]string{"input_tokens_details", "cache_hit_tokens"},
		)
	}
	return nil
}

type openAIError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

type openAIModelList struct {
	Data []openAIModel `json:"data"`
}

type openAIModel struct {
	ID                 string `json:"id"`
	ContextWindowValue int    `json:"context_window"`
	MaxContextLength   int    `json:"max_context_length"`
	MaxInputTokens     int    `json:"max_input_tokens"`
	MaxTokens          int    `json:"max_tokens"`
}

func (m openAIModel) ContextWindow() int {
	for _, value := range []int{m.ContextWindowValue, m.MaxContextLength, m.MaxInputTokens, m.MaxTokens} {
		if value > 0 {
			return value
		}
	}
	return 0
}

// --- stream reading ---

type accumToolCall struct {
	id   string
	name string
	args strings.Builder
}

type streamLine struct {
	line string
	err  error
}

func scanStreamLines(ctx context.Context, body io.ReadCloser, lines chan<- streamLine) {
	defer close(lines)
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		case lines <- streamLine{line: scanner.Text()}:
		}
	}
	if err := scanner.Err(); err != nil {
		select {
		case <-ctx.Done():
		case lines <- streamLine{err: err}:
		}
	}
}

func resetTimer(timer *time.Timer, timeout time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(timeout)
}

func (a *Adapter) readStream(ctx context.Context, cancel context.CancelFunc, body io.ReadCloser, ch chan<- llm.StreamChunk) {
	defer close(ch)
	defer body.Close()
	defer cancel()

	lines := make(chan streamLine, 1)
	go scanStreamLines(ctx, body, lines)

	accums := map[int]*accumToolCall{}
	seenDone := false
	seenData := false
	timeout := a.firstChunkTimeout
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if seenData {
				ch <- llm.StreamChunk{Error: fmt.Errorf("LLM stream idle timeout after %s", a.streamIdleTimeout)}
			} else {
				ch <- llm.StreamChunk{Error: fmt.Errorf("LLM first stream chunk timeout after %s", a.firstChunkTimeout)}
			}
			return
		case item, ok := <-lines:
			if !ok {
				if !seenDone {
					ch <- llm.StreamChunk{Error: fmt.Errorf("read stream: %w", io.ErrUnexpectedEOF)}
				}
				return
			}
			if item.err != nil {
				ch <- llm.StreamChunk{Error: fmt.Errorf("read stream: %w", item.err)}
				return
			}

			line := item.line
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			if line == "data: [DONE]" {
				seenDone = true
				return
			}

			const prefix = "data: "
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			data := strings.TrimPrefix(line, prefix)
			seenData = true
			timeout = a.streamIdleTimeout
			resetTimer(timer, timeout)

			var raw openAIStreamChunk
			if err := json.Unmarshal([]byte(data), &raw); err != nil {
				ch <- llm.StreamChunk{Error: fmt.Errorf("parse stream chunk: %w", err)}
				return
			}

			if raw.Usage != nil && len(raw.Choices) == 0 {
				ch <- llm.StreamChunk{Usage: toUsage(raw.Usage)}
				continue
			}

			if len(raw.Choices) == 0 {
				continue
			}
			choice := raw.Choices[0]

			sc := llm.StreamChunk{
				DeltaContent:          choice.Delta.Content,
				DeltaReasoningContent: choice.Delta.ReasoningContent,
			}
			if choice.FinishReason != nil {
				sc.FinishReason = *choice.FinishReason
			}
			if raw.Usage != nil {
				sc.Usage = toUsage(raw.Usage)
			}

			for _, tc := range choice.Delta.ToolCalls {
				acc := accums[tc.Index]
				if acc == nil {
					acc = &accumToolCall{}
					accums[tc.Index] = acc
				}
				if tc.ID != "" {
					acc.id = tc.ID
				}
				if tc.Function.Name != "" {
					acc.name = tc.Function.Name
				}
				acc.args.WriteString(tc.Function.Arguments)
			}

			if sc.FinishReason != "" && len(accums) > 0 {
				for idx, acc := range accums {
					sc.ToolCallDeltas = append(sc.ToolCallDeltas, llm.ToolCallDelta{
						Index: idx,
						ID:    acc.id,
						Name:  acc.name,
						Args:  acc.args.String(),
					})
				}
			}

			ch <- sc
		}
	}
}

func toUsage(u *openAIUsage) *llm.Usage {
	return &llm.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CacheHitTokens:   u.CacheHitTokens,
	}
}

func firstJSONInt(raw map[string]any, names ...string) int {
	for _, name := range names {
		if value := jsonInt(raw[name]); value > 0 {
			return value
		}
	}
	return 0
}

func firstNestedJSONInt(raw map[string]any, paths ...[]string) int {
	for _, path := range paths {
		var current any = raw
		for _, key := range path {
			object, ok := current.(map[string]any)
			if !ok {
				current = nil
				break
			}
			current = object[key]
		}
		if value := jsonInt(current); value > 0 {
			return value
		}
	}
	return 0
}

func jsonInt(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	default:
		return 0
	}
}

// --- error handling ---

func parseError(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("HTTP %d (failed to read body: %w)", resp.StatusCode, err)
	}

	var apiErr openAIError
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error.Message != "" {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, apiErr.Error.Message)
	}

	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
}
