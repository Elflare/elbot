// 实现网页正文提取工具，管理缓存与共享中的提取请求。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"elbot/internal/llm"
	"elbot/internal/processenv"
	
)

const (
	jinaAPIKeyEnv          = "JINA_API_KEY"
	webExtractProxyEnv     = "WEB_EXTRACT_PROXY"
	defaultExtractEndpoint = "https://r.jina.ai/"
	defaultRemoveSelector  = "header, .class, #id"
	defaultExtractChars    = 8000
	maxExtractChars        = 16000
	defaultExtractTimeout  = 15 * time.Second
	maxExtractCacheEntries = 8
)

type WebExtractTool struct {
	client     *http.Client
	endpoint   string
	cache      *extractCache
	processEnv processenv.Environment
}

type webExtractArgs struct {
	URL            string `json:"url"`
	Offset         int    `json:"offset"`
	MaxChars       int    `json:"max_chars"`
	RemoveSelector string `json:"remove_selector"`
	TimeoutMS      int    `json:"timeout_ms"`
	Proxy          string `json:"proxy"`
	Jina           *bool  `json:"jina"`
	ForceRefresh   bool   `json:"force_refresh"`
}

type jinaResponse struct {
	URL       string            `json:"url"`
	Title     string            `json:"title"`
	Content   string            `json:"content"`
	Timestamp string            `json:"timestamp"`
	Data      *jinaResponseData `json:"data"`
}

type jinaResponseData struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

type webExtractData struct {
	URL           string `json:"url"`
	Title         string `json:"title,omitempty"`
	Source        string `json:"source"`
	TotalChars    int    `json:"total_chars"`
	Offset        int    `json:"offset"`
	ReturnedChars int    `json:"returned_chars"`
	NextOffset    *int   `json:"next_offset,omitempty"`
	Truncated     bool   `json:"truncated"`
	Cached        bool   `json:"cached"`
	Content       string `json:"content"`
}

type cachedExtract struct {
	url       string
	title     string
	source    string
	content   string
	fetchedAt time.Time
}

type extractCache struct {
	mu     sync.Mutex
	order  []string
	values map[string]cachedExtract
	// inflight shares unfinished fetches by cache key; mu protects all flight state.
	inflight map[string]*extractFlight
}

type extractFlight struct {
	// done publishes entry and err when the fetch finishes.
	done chan struct{}
	// cancel stops the shared fetch after its last waiter leaves.
	cancel context.CancelFunc
	// waiters counts callers still waiting for this unfinished fetch.
	waiters int
	// entry and err hold the completed fetch outcome, published through done.
	entry cachedExtract
	err   error
}

type extractProxyConfig struct {
	proxyURL *url.URL
	disabled bool
	cacheKey string
}

func NewWebExtractTool(environment ...processenv.Environment) *WebExtractTool {
	var processEnv processenv.Environment
	if len(environment) > 0 {
		processEnv = environment[0]
	}
	return &WebExtractTool{endpoint: defaultExtractEndpoint, cache: newExtractCache(), processEnv: processEnv}
}

func (*WebExtractTool) Name() string {
	return "web_extract"
}

func (t *WebExtractTool) Info() toolshare.Info {
	return webExtractBuilder().BuildInfo()
}

func (t *WebExtractTool) Schema() llm.ToolSchema {
	return webExtractBuilder().BuildSchema()
}

func webExtractBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("web_extract").
		Description("提取网页正文；默认返回前 8000 字符，可用同一 URL 加 offset 继续读取后续内容。").
		Risk(toolshare.RiskLow).
		Tags("web").
		DependsOn("web_search").
		String("url", "要提取的网页 URL。", toolshare.Required()).
		Integer("offset", "从第几个字符开始返回，默认 0；用于继续读取缓存中的后续内容。").
		Integer("max_chars", "本次最多返回字符数，默认 8000，硬上限 16000。").
		String("remove_selector", "可选，覆盖默认移除 CSS 选择器：header, .class, #id。").
		Integer("timeout_ms", "可选，请求超时时间，默认 15000。").
		String("proxy", "代理设置：不填则使用 WEB_EXTRACT_PROXY 或系统代理；填 disabled 禁用代理；填 URL 使用指定代理。").
		Boolean("jina", "是否使用 Jina Reader 提取网页，默认 true；Jina 失败或效果不佳时可传 false 改用直接爬取。").
		Boolean("force_refresh", "是否跳过缓存并重新获取网页，默认 false；刷新成功后会覆盖对应缓存。")
}

func (t *WebExtractTool) Call(ctx context.Context, req toolshare.CallRequest) (_ *toolshare.Result, logContent string, _ error) {
	defer func() { logContent = toolshare.TruncateLogContent(logContent, 1024) }()
	if err := ctx.Err(); err != nil {
		return nil, err.Error(), err
	}
	var args webExtractArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			{
				callErr := fmt.Errorf("parse web_extract arguments: %w", err)
				return nil, callErr.Error(), callErr
			}
		}
	}
	url := strings.TrimSpace(args.URL)
	if url == "" {
		{
			callErr := fmt.Errorf("url is required")
			return nil, callErr.Error(), callErr
		}
	}
	selector := strings.TrimSpace(args.RemoveSelector)
	if selector == "" {
		selector = defaultRemoveSelector
	}
	proxy, err := resolveExtractProxy(t.processEnv, args.Proxy)
	if err != nil {
		return nil, err.Error(), err
	}
	cache := t.cache
	useJina := true
	if args.Jina != nil {
		useJina = *args.Jina
	}
	apiKey := ""
	hasJinaKey := false
	if useJina {
		apiKey, hasJinaKey = t.processEnv.Lookup(jinaAPIKeyEnv)
		hasJinaKey = hasJinaKey && strings.TrimSpace(apiKey) != ""
	}
	source := "direct"
	if useJina && hasJinaKey {
		source = "jina"
	}
	key := extractCacheKey(source, url, selector, proxy)
	timeout := defaultExtractTimeout
	if args.TimeoutMS > 0 {
		timeout = time.Duration(args.TimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	entry, cached, err := cache.getOrFetch(ctx, key, args.ForceRefresh, func(fetchCtx context.Context) (cachedExtract, error) {
		if useJina && hasJinaKey {
			return t.fetchJina(fetchCtx, apiKey, url, selector, proxy)
		}
		return t.fetchDirect(fetchCtx, url, proxy)
	})
	if err != nil {
		return nil, err.Error(), err
	}
	data := sliceExtract(entry, args.Offset, args.MaxChars, cached)
	{
		result := &toolshare.Result{Content: formatExtractContent(data)}
		return result, fmt.Sprintf("url=%s range=%d-%d/%d cached=%t source=%s\n%s\n%s", data.URL, data.Offset, data.Offset+data.ReturnedChars, data.TotalChars, data.Cached, data.Source, data.Title, data.Content), nil
	}
}

func (t *WebExtractTool) fetchJina(ctx context.Context, apiKey, targetURL, selector string, proxy extractProxyConfig) (cachedExtract, error) {
	readerURL, err := jinaReaderURL(t.extractEndpoint(), targetURL)
	if err != nil {
		return cachedExtract{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, readerURL, nil)
	if err != nil {
		return cachedExtract{}, fmt.Errorf("create Jina request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("X-Respond-With", "json")
	httpReq.Header.Set("X-Remove-Selector", selector)
	httpReq.Header.Set("X-Robots-Txt", "JinaReader")
	httpReq.Header.Set("X-With-Generated-Alt", "true")
	httpReq.Header.Set("X-With-Images-Summary", "true")
	httpReq.Header.Set("X-With-Links-Summary", "true")

	resp, err := t.httpClient(proxy).Do(httpReq)
	if err != nil {
		return cachedExtract{}, fmt.Errorf("Jina request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return cachedExtract{}, fmt.Errorf("read Jina response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cachedExtract{}, fmt.Errorf("Jina HTTP %d: %s", resp.StatusCode, truncateHTTPBody(respBody))
	}
	var data jinaResponse
	if err := json.Unmarshal(respBody, &data); err != nil {
		return cachedExtract{}, fmt.Errorf("parse Jina response: %w", err)
	}
	return normalizeJinaResponse(data, targetURL), nil
}

func normalizeJinaResponse(data jinaResponse, fallbackURL string) cachedExtract {
	if data.Data != nil {
		data.URL = data.Data.URL
		data.Title = data.Data.Title
		data.Content = data.Data.Content
		data.Timestamp = data.Data.Timestamp
	}
	if data.URL == "" {
		data.URL = fallbackURL
	}
	return cachedExtract{url: data.URL, title: data.Title, source: "jina", content: data.Content, fetchedAt: time.Now()}
}

func jinaReaderURL(endpoint, targetURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(targetURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid url: %s", targetURL)
	}
	return strings.TrimRight(endpoint, "/") + "/http://" + parsed.Host + parsed.RequestURI(), nil
}

func (t *WebExtractTool) fetchDirect(ctx context.Context, targetURL string, proxy extractProxyConfig) (cachedExtract, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return cachedExtract{}, fmt.Errorf("create direct extract request: %w", err)
	}
	httpReq.Header.Set("User-Agent", "ElBot-WebExtract/1.0")
	resp, err := t.httpClient(proxy).Do(httpReq)
	if err != nil {
		return cachedExtract{}, fmt.Errorf("direct extract request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return cachedExtract{}, fmt.Errorf("read direct extract response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cachedExtract{}, fmt.Errorf("direct extract HTTP %d: %s", resp.StatusCode, truncateHTTPBody(respBody))
	}
	title, content := extractSimpleHTMLText(string(respBody))
	return cachedExtract{url: targetURL, title: title, source: "direct", content: content, fetchedAt: time.Now()}, nil
}

func sliceExtract(entry cachedExtract, offset, maxChars int, cached bool) webExtractData {
	if offset < 0 {
		offset = 0
	}
	if maxChars <= 0 {
		maxChars = defaultExtractChars
	}
	if maxChars > maxExtractChars {
		maxChars = maxExtractChars
	}
	contentRunes := []rune(entry.content)
	total := len(contentRunes)
	if offset > total {
		offset = total
	}
	end := offset + maxChars
	if end > total {
		end = total
	}
	content := string(contentRunes[offset:end])
	returned := end - offset
	var next *int
	if end < total {
		value := end
		next = &value
	}
	return webExtractData{
		URL:           entry.url,
		Title:         entry.title,
		Source:        entry.source,
		TotalChars:    total,
		Offset:        offset,
		ReturnedChars: returned,
		NextOffset:    next,
		Truncated:     next != nil,
		Cached:        cached,
		Content:       content,
	}
}

func formatExtractContent(data webExtractData) string {
	var out strings.Builder
	out.WriteString("URL: ")
	out.WriteString(data.URL)
	if data.Source != "" {
		out.WriteString("\nSource: ")
		out.WriteString(data.Source)
	}
	if data.Title != "" {
		out.WriteString("\nTitle: ")
		out.WriteString(data.Title)
	}
	out.WriteString(fmt.Sprintf("\nRange: %d-%d / %d", data.Offset, data.Offset+data.ReturnedChars, data.TotalChars))
	if data.NextOffset != nil {
		out.WriteString(fmt.Sprintf("\nNext offset: %d", *data.NextOffset))
	}
	out.WriteString(fmt.Sprintf("\nCached: %t\n\n", data.Cached))
	out.WriteString(data.Content)
	return strings.TrimSpace(out.String())
}

func (t *WebExtractTool) httpClient(proxy extractProxyConfig) *http.Client {
	if t.client != nil {
		return t.client
	}
	return &http.Client{Transport: extractTransport(proxy)}
}

func resolveExtractProxy(environment processenv.Environment, value string) (extractProxyConfig, error) {
	proxy := strings.TrimSpace(value)
	if proxy == "" {
		if configured, ok := environment.Lookup(webExtractProxyEnv); ok {
			proxy = strings.TrimSpace(configured)
		}
	}
	if proxy == "" {
		return extractProxyConfig{cacheKey: "env"}, nil
	}
	if proxy == "disabled" {
		return extractProxyConfig{disabled: true, cacheKey: "disabled"}, nil
	}
	proxyURL, err := url.Parse(proxy)
	if err != nil || proxyURL.Scheme == "" || proxyURL.Host == "" {
		return extractProxyConfig{}, fmt.Errorf("invalid web_extract proxy url: %s", proxy)
	}
	return extractProxyConfig{proxyURL: proxyURL, cacheKey: proxyURL.String()}, nil
}

func extractTransport(proxy extractProxyConfig) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if proxy.disabled {
		transport.Proxy = nil
		return transport
	}
	if proxy.proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxy.proxyURL)
		return transport
	}
	transport.Proxy = http.ProxyFromEnvironment
	return transport
}

func extractSimpleHTMLText(raw string) (string, string) {
	title := strings.TrimSpace(html.UnescapeString(stripTags(extractTagContent(raw, "title"))))
	text := raw
	for _, tag := range []string{"script", "style", "noscript"} {
		text = removeTagBlocks(text, tag)
	}
	text = removeHTMLComments(text)
	text = stripTags(text)
	text = html.UnescapeString(text)
	text = strings.Join(strings.Fields(text), " ")
	return title, text
}

func extractTagContent(raw, tag string) string {
	lower := strings.ToLower(raw)
	open := "<" + tag
	start := strings.Index(lower, open)
	if start < 0 {
		return ""
	}
	openEnd := strings.Index(lower[start:], ">")
	if openEnd < 0 {
		return ""
	}
	contentStart := start + openEnd + 1
	close := strings.Index(lower[contentStart:], "</"+tag+">")
	if close < 0 {
		return ""
	}
	return raw[contentStart : contentStart+close]
}

func removeTagBlocks(raw, tag string) string {
	for {
		lower := strings.ToLower(raw)
		open := strings.Index(lower, "<"+tag)
		if open < 0 {
			return raw
		}
		closeStart := strings.Index(lower[open:], "</"+tag+">")
		if closeStart < 0 {
			return raw[:open]
		}
		end := open + closeStart + len(tag) + 3
		raw = raw[:open] + " " + raw[end:]
	}
}

func removeHTMLComments(raw string) string {
	for {
		start := strings.Index(raw, "<!--")
		if start < 0 {
			return raw
		}
		end := strings.Index(raw[start+4:], "-->")
		if end < 0 {
			return raw[:start]
		}
		raw = raw[:start] + " " + raw[start+4+end+3:]
	}
}

func stripTags(raw string) string {
	var out strings.Builder
	inTag := false
	for _, r := range raw {
		switch r {
		case '<':
			inTag = true
			out.WriteRune(' ')
		case '>':
			inTag = false
			out.WriteRune(' ')
		default:
			if !inTag {
				out.WriteRune(r)
			}
		}
	}
	return out.String()
}

func (t *WebExtractTool) extractEndpoint() string {
	if strings.TrimSpace(t.endpoint) != "" {
		return t.endpoint
	}
	return defaultExtractEndpoint
}

func newExtractCache() *extractCache {
	return &extractCache{values: map[string]cachedExtract{}, inflight: map[string]*extractFlight{}}
}

func (c *extractCache) getOrFetch(ctx context.Context, key string, forceRefresh bool, fetch func(context.Context) (cachedExtract, error)) (cachedExtract, bool, error) {
	c.mu.Lock()
	if entry, ok := c.values[key]; ok && !forceRefresh {
		c.mu.Unlock()
		return entry, true, nil
	}
	flight, exists := c.inflight[key]
	if !exists {
		fetchCtx, cancel := context.WithTimeout(context.Background(), defaultExtractTimeout)
		flight = &extractFlight{done: make(chan struct{}), cancel: cancel}
		c.inflight[key] = flight
		go func() {
			entry, err := fetch(fetchCtx)
			cancel()
			c.mu.Lock()
			defer c.mu.Unlock()
			flight.entry, flight.err = entry, err
			// A canceled flight may finish after a replacement has already started.
			if c.inflight[key] == flight {
				if err == nil {
					c.setLocked(key, entry)
				}
				delete(c.inflight, key)
			}
			close(flight.done)
		}()
	}
	flight.waiters++
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		c.mu.Lock()
		flight.waiters--
		if flight.waiters == 0 && c.inflight[key] == flight {
			delete(c.inflight, key)
			flight.cancel()
		}
		c.mu.Unlock()
		return cachedExtract{}, false, ctx.Err()
	case <-flight.done:
		if err := ctx.Err(); err != nil {
			return cachedExtract{}, false, err
		}
		return flight.entry, false, flight.err
	}
}

func (c *extractCache) setLocked(key string, entry cachedExtract) {
	if _, exists := c.values[key]; exists {
		c.values[key] = entry
		return
	}
	if len(c.order) >= maxExtractCacheEntries {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.values, oldest)
	}
	c.order = append(c.order, key)
	c.values[key] = entry
}

func extractCacheKey(source, targetURL, selector string, proxy extractProxyConfig) string {
	return strings.TrimSpace(source) + "\x00" + strings.TrimSpace(targetURL) + "\x00" + strings.TrimSpace(selector) + "\x00" + proxy.cacheKey
}
