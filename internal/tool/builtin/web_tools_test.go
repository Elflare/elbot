package builtin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/tool"
)

func TestBuiltinEnvReadsOSEnvFirst(t *testing.T) {
	t.Setenv("ELBOT_TEST_KEY", "from-os")
	value, err := builtinEnv(context.Background(), "ELBOT_TEST_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if value != "from-os" {
		t.Fatalf("value = %q", value)
	}
}

func TestBuiltinEnvReadsConfigDotEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("ELBOT_TEST_DOT='from-dotenv'\n"), 0600); err != nil {
		t.Fatal(err)
	}

	ctx := WithConfigEnvDir(context.Background(), dir)
	value, err := builtinEnv(ctx, "ELBOT_TEST_DOT")
	if err != nil {
		t.Fatal(err)
	}
	if value != "from-dotenv" {
		t.Fatalf("value = %q", value)
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
	result, err := search.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"query":"elbot"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "answer") || !strings.Contains(result.Content, "https://example.com") {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestWebSearchToolRequiresKey(t *testing.T) {
	t.Setenv(tavilyAPIKeyEnv, "")

	_, err := NewWebSearchTool().Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"query":"elbot"}`)})
	if err == nil || !strings.Contains(err.Error(), tavilyAPIKeyEnv) {
		t.Fatalf("err = %v", err)
	}
}

func TestWebExtractToolSlicesAndCaches(t *testing.T) {
	t.Setenv(jinaAPIKeyEnv, "jina-test")
	calls := 0
	content := strings.Repeat("a", defaultExtractChars) + strings.Repeat("b", 2000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
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
	first, err := extract.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"url":"https://example.com/page"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Content, "Source: jina") || !strings.Contains(first.Content, "Title: Example") || !strings.Contains(first.Content, "Next offset: 8000") || !strings.Contains(first.Content, "Cached: false") {
		t.Fatalf("first content = %q", first.Content)
	}

	second, err := extract.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"url":"https://example.com/page","offset":8000}`)})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected cached second call, server calls = %d", calls)
	}
	if !strings.Contains(second.Content, "Range: 8000-10000 / 10000") || !strings.Contains(second.Content, "Cached: true") || !strings.Contains(second.Content, strings.Repeat("b", 20)) {
		t.Fatalf("second content = %q", second.Content)
	}
}

func TestWebExtractDefaultClientUsesConfiguredProxy(t *testing.T) {
	t.Setenv(webExtractProxyEnv, "http://127.0.0.1:9999")
	proxy, err := resolveExtractProxy(context.Background(), "")
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
	proxy, err := resolveExtractProxy(context.Background(), "http://127.0.0.1:7891")
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
	proxy, err := resolveExtractProxy(context.Background(), "disabled")
	if err != nil {
		t.Fatal(err)
	}
	transport := extractTransport(proxy)
	if transport.Proxy != nil {
		t.Fatal("expected nil proxy")
	}
}

func TestWebExtractRejectsInvalidProxyURL(t *testing.T) {
	_, err := resolveExtractProxy(context.Background(), "localhost:7890")
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
	result, err := extract.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"url":"` + server.URL + `","proxy":"disabled"}`)})
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
	result, err := extract.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"url":"` + server.URL + `","jina":false}`)})
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
