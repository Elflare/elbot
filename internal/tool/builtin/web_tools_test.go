package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/processenv"
	"elbot/internal/tool"
)

func TestWebSearchToolReadsSharedEnvironment(t *testing.T) {
	environment := processenv.New([]string{"TAVILY_API_KEY=tvly-configured"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tvly-configured" {
			t.Fatalf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(tavilyResponse{Query: "configured"})
	}))
	defer server.Close()

	search := NewWebSearchTool(environment)
	search.endpoint = server.URL
	if _, _, err := search.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"query":"configured"}`)}); err != nil {
		t.Fatal(err)
	}
}

func TestWebSearchToolSendsDefaults(t *testing.T) {
	t.Setenv(tavilyAPIKeyEnv, "tvly-test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tvly-test" {
			t.Fatalf("Authorization = %q", got)
		}
		var body tavilyRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Query != "elbot" || body.MaxResults != defaultSearchResults || body.SearchDepth != defaultSearchDepth {
			t.Fatalf("request body = %#v", body)
		}
		if !body.IncludeAnswer || body.IncludeRawContent || body.IncludeImages || body.AutoParameters {
			t.Fatalf("unexpected fixed flags = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(tavilyResponse{Query: body.Query, Answer: "answer", Results: []tavilyResult{{Title: "Title", URL: "https://example.com", Content: "summary"}}})
	}))
	defer server.Close()

	search := NewWebSearchTool()
	search.endpoint = server.URL
	result, logContent, err := search.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"query":"elbot"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "answer") || !strings.Contains(result.Content, "https://example.com") {
		t.Fatalf("content = %q", result.Content)
	}
	if !strings.Contains(logContent, "results=1") || !strings.Contains(logContent, "Title https://example.com") || strings.Contains(logContent, "summary") || strings.Contains(logContent, "answer") || len([]rune(logContent)) > 1024 {
		t.Fatalf("search log should retain sources without body: %q", logContent)
	}
}

func TestWebSearchToolRequiresKey(t *testing.T) {
	t.Setenv(tavilyAPIKeyEnv, "")

	_, _, err := NewWebSearchTool().Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"query":"elbot"}`)})
	if err == nil || !strings.Contains(err.Error(), tavilyAPIKeyEnv) {
		t.Fatalf("err = %v", err)
	}
}

func TestWebExtractToolSlicesAndCaches(t *testing.T) {
	t.Setenv(jinaAPIKeyEnv, "jina-test")
	content := strings.Repeat("a", defaultExtractChars) + strings.Repeat("b", 2000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s", r.Method)
		}
		if r.URL.String() != "/http://example.com/page" {
			t.Fatalf("reader URL = %q", r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer jina-test" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "" {
			t.Fatalf("Content-Type = %q", got)
		}
		if got := r.Header.Get("X-Remove-Selector"); got != defaultRemoveSelector {
			t.Fatalf("X-Remove-Selector = %q", got)
		}
		_ = json.NewEncoder(w).Encode(jinaResponse{Data: &jinaResponseData{URL: "https://example.com/page", Title: "Example", Content: content}})
	}))
	defer server.Close()

	extract := NewWebExtractTool()
	extract.client = server.Client()
	extract.endpoint = server.URL
	first, logContent, err := extract.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"url":"https://example.com/page"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Content, "Source: jina") || !strings.Contains(first.Content, "Title: Example") || !strings.Contains(first.Content, "Next offset: 8000") || !strings.Contains(first.Content, "Cached: false") {
		t.Fatalf("first content = %q", first.Content)
	}
	if len([]rune(logContent)) != 1024 || !strings.HasSuffix(logContent, "…") || !strings.Contains(logContent, "range=0-8000/10000 cached=false") {
		t.Fatalf("extract log=%q", logContent)
	}

	second, _, err := extract.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"url":"https://example.com/page","offset":8000}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.Content, "Range: 8000-10000 / 10000") || !strings.Contains(second.Content, "Cached: true") || !strings.Contains(second.Content, strings.Repeat("b", 20)) {
		t.Fatalf("second content = %q", second.Content)
	}
}

func TestWebExtractToolForceRefreshReplacesCache(t *testing.T) {
	t.Setenv(jinaAPIKeyEnv, "jina-test")
	var refreshed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		content := "first version"
		if refreshed.Load() {
			content = "second version"
		}
		_ = json.NewEncoder(w).Encode(jinaResponse{Data: &jinaResponseData{URL: "https://example.com/page", Content: content}})
	}))
	defer server.Close()

	extract := NewWebExtractTool()
	extract.client = server.Client()
	extract.endpoint = server.URL
	first, _, err := extract.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"url":"https://example.com/page"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Content, "first version") || !strings.Contains(first.Content, "Cached: false") {
		t.Fatalf("first content = %q", first.Content)
	}

	refreshed.Store(true)
	result, _, err := extract.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"url":"https://example.com/page","force_refresh":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "second version") || !strings.Contains(result.Content, "Cached: false") {
		t.Fatalf("refreshed content = %q", result.Content)
	}

	cached, _, err := extract.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"url":"https://example.com/page"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cached.Content, "second version") || !strings.Contains(cached.Content, "Cached: true") {
		t.Fatalf("cached content = %q", cached.Content)
	}
}

func TestWebExtractToolFailedForceRefreshPreservesCache(t *testing.T) {
	t.Setenv(jinaAPIKeyEnv, "jina-test")
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			http.Error(w, "refresh failed", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(jinaResponse{Data: &jinaResponseData{URL: "https://example.com/page", Content: "cached version"}})
	}))
	defer server.Close()

	extract := NewWebExtractTool()
	extract.client = server.Client()
	extract.endpoint = server.URL
	if _, _, err := extract.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"url":"https://example.com/page"}`)}); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	if _, _, err := extract.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"url":"https://example.com/page","force_refresh":true}`)}); err == nil {
		t.Fatal("expected force refresh error")
	}

	cached, _, err := extract.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"url":"https://example.com/page"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cached.Content, "cached version") || !strings.Contains(cached.Content, "Cached: true") {
		t.Fatalf("cached content = %q", cached.Content)
	}
}

func TestWebExtractDefaultClientUsesConfiguredProxy(t *testing.T) {
	environment := processenv.New([]string{webExtractProxyEnv + "=http://127.0.0.1:9999"})
	proxy, err := resolveExtractProxy(environment, "")
	if err != nil {
		t.Fatal(err)
	}
	extract := NewWebExtractTool()
	transport, ok := extract.httpClient(proxy).Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		t.Fatalf("transport = %#v", extract.httpClient(proxy).Transport)
	}
	req := &http.Request{URL: mustParseURL(t, "https://example.com")}
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if proxyURL == nil || proxyURL.String() != "http://127.0.0.1:9999" {
		t.Fatalf("proxy = %v", proxyURL)
	}
}

// Wait for callers to join before testing cancellation or publishing a response.
// This synchronizes the scenario; HTTP request counts are not asserted.
func waitForExtractWaiters(t *testing.T, cache *extractCache, key string, want int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		cache.mu.Lock()
		flight := cache.inflight[key]
		ready := flight != nil && flight.waiters == want
		cache.mu.Unlock()
		if ready {
			return
		}
		select {
		case <-deadline:
			t.Fatal("callers did not reach the shared fetch")
		case <-ticker.C:
		}
	}
}

func TestWebExtractConcurrentPaginationAndCancellation(t *testing.T) {
	for _, scenario := range []string{"pagination", "first canceled", "second canceled", "first timeout", "second timeout"} {
		t.Run(scenario, func(t *testing.T) {
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-release:
					_, _ = w.Write([]byte("<html><title>Example</title><body>abcdef</body></html>"))
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			extract := NewWebExtractTool()
			extract.client = server.Client()
			firstCtx, firstCancel := context.WithCancel(t.Context())
			defer firstCancel()
			secondCtx, secondCancel := context.WithCancel(t.Context())
			defer secondCancel()
			type outcome struct {
				result *tool.Result
				err    error
			}
			call := func(ctx context.Context, first bool) <-chan outcome {
				out := make(chan outcome, 1)
				args := map[string]any{"url": server.URL, "jina": false, "proxy": "disabled", "max_chars": 3, "timeout_ms": 3000}
				if !first {
					args["offset"] = 3
					args["force_refresh"] = true
				}
				if first && scenario == "first timeout" || !first && scenario == "second timeout" {
					args["timeout_ms"] = 300
				}
				raw, _ := json.Marshal(args)
				go func() {
					result, _, err := extract.Call(ctx, tool.CallRequest{Arguments: raw})
					out <- outcome{result, err}
				}()
				return out
			}
			first := call(firstCtx, true)
			key := extractCacheKey("direct", server.URL, defaultRemoveSelector, extractProxyConfig{cacheKey: "disabled"})
			waitForExtractWaiters(t, extract.cache, key, 1)
			second := call(secondCtx, false)
			waitForExtractWaiters(t, extract.cache, key, 2)
			read := func(out <-chan outcome) outcome {
				t.Helper()
				select {
				case result := <-out:
					return result
				case <-time.After(4 * time.Second):
					t.Fatal("extract did not finish")
					return outcome{}
				}
			}
			if scenario != "pagination" {
				canceled := first
				if strings.HasPrefix(scenario, "second") {
					canceled = second
				}
				wantErr := context.DeadlineExceeded
				if scenario == "first canceled" {
					firstCancel()
					wantErr = context.Canceled
				} else if scenario == "second canceled" {
					secondCancel()
					wantErr = context.Canceled
				}
				if result := read(canceled); !errors.Is(result.err, wantErr) {
					t.Fatalf("canceled caller = %v; want %v", result.err, wantErr)
				}
			}
			close(release)
			for i, out := range []<-chan outcome{first, second} {
				if i == 0 && strings.HasPrefix(scenario, "first") || i == 1 && strings.HasPrefix(scenario, "second") {
					continue
				}
				result := read(out)
				want := "Range: 0-3 / 14"
				if i == 1 {
					want = "Range: 3-6 / 14"
				}
				if result.err != nil || !strings.Contains(result.result.Content, want) || !strings.Contains(result.result.Content, "Cached: false") {
					t.Fatalf("caller %d = %#v, %v", i, result.result, result.err)
				}
			}
		})
	}
}

func TestExtractCacheCanceledFetchCannotReplaceNewResult(t *testing.T) {
	cache := newExtractCache()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	defer close(release)
	first := make(chan error, 1)
	go func() {
		_, _, err := cache.getOrFetch(ctx, "page", false, func(fetchCtx context.Context) (cachedExtract, error) {
			started <- fetchCtx
			<-release // Model a transport returning a late response after cancellation.
			return cachedExtract{content: "old"}, nil
		})
		first <- err
	}()
	fetchCtx := <-started
	cache.mu.Lock()
	flight := cache.inflight["page"]
	cache.mu.Unlock()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first call = %v", err)
	}
	select {
	case <-fetchCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("fetch remained active without waiters")
	}
	fetchNew := func(context.Context) (cachedExtract, error) { return cachedExtract{content: "new"}, nil }
	entry, cached, err := cache.getOrFetch(t.Context(), "page", false, fetchNew)
	if err != nil || cached || entry.content != "new" {
		t.Fatalf("replacement = %#v, %t, %v", entry, cached, err)
	}
	// Unblock the canceled transport, then wait until its result has been processed.
	release <- struct{}{}
	<-flight.done
	entry, cached, err = cache.getOrFetch(t.Context(), "page", false, fetchNew)
	if err != nil || !cached || entry.content != "new" {
		t.Fatalf("cache after late completion = %#v, %t, %v", entry, cached, err)
	}
}

func TestExtractCacheRefreshAndFailureRecovery(t *testing.T) {
	cache := newExtractCache()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	failure := errors.New("upstream failed")
	_, _, err := cache.getOrFetch(ctx, "page", false, func(context.Context) (cachedExtract, error) { return cachedExtract{}, failure })
	if !errors.Is(err, failure) {
		t.Fatalf("failed fetch = %v", err)
	}
	old := func(context.Context) (cachedExtract, error) { return cachedExtract{content: "old"}, nil }
	if entry, cached, err := cache.getOrFetch(ctx, "page", false, old); err != nil || cached || entry.content != "old" {
		t.Fatalf("retry = %#v, %t, %v", entry, cached, err)
	}
	release := make(chan struct{})
	defer close(release)
	refresh := make(chan error, 1)
	go func() {
		_, _, err := cache.getOrFetch(ctx, "page", true, func(fetchCtx context.Context) (cachedExtract, error) {
			select {
			case <-release:
				return cachedExtract{content: "new"}, nil
			case <-fetchCtx.Done():
				return cachedExtract{}, fetchCtx.Err()
			}
		})
		refresh <- err
	}()
	waitForExtractWaiters(t, cache, "page", 1)
	if entry, cached, err := cache.getOrFetch(ctx, "page", false, old); err != nil || !cached || entry.content != "old" {
		t.Fatalf("read during refresh = %#v, %t, %v", entry, cached, err)
	}
	if entry, _, err := cache.getOrFetch(ctx, "other", false, old); err != nil || entry.content != "old" {
		t.Fatalf("other key during refresh = %#v, %v", entry, err)
	}
	release <- struct{}{}
	if err := <-refresh; err != nil {
		t.Fatal(err)
	}
	if entry, cached, err := cache.getOrFetch(ctx, "page", false, old); err != nil || !cached || entry.content != "new" {
		t.Fatalf("refreshed cache = %#v, %t, %v", entry, cached, err)
	}
}

func TestJinaReaderURL(t *testing.T) {
	got, err := jinaReaderURL("https://r.jina.ai/", "https://github.com/rimeinn/rime-moran?tab=readme")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://r.jina.ai/http://github.com/rimeinn/rime-moran?tab=readme"
	if got != want {
		t.Fatalf("url = %q want %q", got, want)
	}
}

func TestWebExtractToolParsesFlatJinaResponse(t *testing.T) {
	entry := normalizeJinaResponse(jinaResponse{URL: "https://example.com", Title: "Flat", Content: "正文"}, "fallback")
	if entry.url != "https://example.com" || entry.title != "Flat" || entry.content != "正文" || entry.source != "jina" {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestWebExtractTransportUsesProxyArgument(t *testing.T) {
	proxy, err := resolveExtractProxy(processenv.Environment{}, "http://127.0.0.1:7891")
	if err != nil {
		t.Fatal(err)
	}
	transport := extractTransport(proxy)
	if transport.Proxy == nil {
		t.Fatal("expected proxy")
	}
	req := &http.Request{URL: mustParseURL(t, "https://example.com")}
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if proxyURL == nil || proxyURL.String() != "http://127.0.0.1:7891" {
		t.Fatalf("proxy = %v", proxyURL)
	}
}

func TestWebExtractTransportDisablesProxyWithArgument(t *testing.T) {
	proxy, err := resolveExtractProxy(processenv.Environment{}, "disabled")
	if err != nil {
		t.Fatal(err)
	}
	transport := extractTransport(proxy)
	if transport.Proxy != nil {
		t.Fatal("expected nil proxy")
	}
}

func TestWebExtractRejectsInvalidProxyURL(t *testing.T) {
	_, err := resolveExtractProxy(processenv.Environment{}, "localhost:7890")
	if err == nil || !strings.Contains(err.Error(), "invalid web_extract proxy url") {
		t.Fatalf("err = %v", err)
	}
}

func TestWebExtractToolDirectFallbackWithoutJinaKey(t *testing.T) {
	t.Setenv(jinaAPIKeyEnv, "")
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>测试页</title><style>.x{}</style><script>alert(1)</script></head><body><h1>标题</h1><!-- hidden --><p>甲&nbsp;&amp;&nbsp;乙</p></body></html>`))
	}))
	defer server.Close()

	extract := NewWebExtractTool()
	result, _, err := extract.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"url":"` + server.URL + `","proxy":"disabled"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "Source: direct") || !strings.Contains(result.Content, "Title: 测试页") {
		t.Fatalf("content = %q", result.Content)
	}
	if strings.Contains(result.Content, "alert") || strings.Contains(result.Content, "hidden") || !strings.Contains(result.Content, "甲 & 乙") {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestWebExtractToolCanDisableJina(t *testing.T) {
	t.Setenv(jinaAPIKeyEnv, "jina-test")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Direct</title></head><body><p>直接提取</p></body></html>`))
	}))
	defer server.Close()

	extract := NewWebExtractTool()
	extract.client = server.Client()
	extract.endpoint = "http://127.0.0.1:1"
	result, _, err := extract.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"url":"` + server.URL + `","jina":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("server calls = %d", calls)
	}
	if !strings.Contains(result.Content, "Source: direct") || !strings.Contains(result.Content, "Title: Direct") || !strings.Contains(result.Content, "直接提取") {
		t.Fatalf("content = %q", result.Content)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestWebExtractToolCapsMaxChars(t *testing.T) {
	data := sliceExtract(cachedExtract{url: "u", content: strings.Repeat("x", maxExtractChars+100)}, 0, maxExtractChars+999, true)
	if data.ReturnedChars != maxExtractChars || data.NextOffset == nil {
		t.Fatalf("data = %#v", data)
	}
}

func TestWebExtractToolSlicesRunes(t *testing.T) {
	data := sliceExtract(cachedExtract{url: "u", content: "甲乙丙丁"}, 1, 2, true)
	if data.Content != "乙丙" || data.TotalChars != 4 || data.ReturnedChars != 2 {
		t.Fatalf("data = %#v", data)
	}
}
