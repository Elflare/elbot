// 请求网页提取服务，解析响应并处理 HTTP 与代理配置。
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"elbot/internal/processenv"
)

const (
	jinaAPIKeyEnv              = "JINA_API_KEY"
	webExtractProxyEnv         = "WEB_EXTRACT_PROXY"
	defaultExtractEndpoint     = "https://r.jina.ai/"
	defaultMarkdownNewEndpoint = "https://markdown.new/"
	defaultRemoveSelector      = "header, .class, #id"
)

type jinaResponse struct {
	URL     string            `json:"url"`
	Title   string            `json:"title"`
	Content string            `json:"content"`
	Data    *jinaResponseData `json:"data"`
}

type jinaResponseData struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

type markdownNewResponse struct {
	// Success 表示服务是否成功完成转换；HTTP 200 本身不代表转换成功。
	Success bool   `json:"success"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

type extractProxyConfig struct {
	proxyURL *url.URL
	disabled bool
	cacheKey string
}

func (t *WebExtractTool) fetchJina(ctx context.Context, apiKey, targetURL, selector string, proxy extractProxyConfig) (cachedExtract, error) {
	readerURL, err := jinaReaderURL(t.endpoint, targetURL)
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
	if resp.StatusCode == http.StatusPaymentRequired {
		return cachedExtract{}, fmt.Errorf("Jina 账户余额不足，请告知用户需要充值；如需继续提取，可设置 method=direct 改用直接抓取，或 method=markdownnew 使用 markdown.new。Jina HTTP %d: %s", resp.StatusCode, truncateHTTPBody(respBody))
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
	}
	if data.URL == "" {
		data.URL = fallbackURL
	}
	return cachedExtract{url: data.URL, title: data.Title, source: "jina", content: data.Content}
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
	title, content := extractSimpleHTMLText(string(respBody), resp.Request.URL)
	return cachedExtract{url: targetURL, title: title, source: "direct", content: content}, nil
}

func (t *WebExtractTool) fetchMarkdownNew(ctx context.Context, targetURL string, proxy extractProxyConfig) (cachedExtract, error) {
	// 字符串映射始终可序列化。
	body, _ := json.Marshal(map[string]string{"url": targetURL})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, defaultMarkdownNewEndpoint, bytes.NewReader(body))
	if err != nil {
		return cachedExtract{}, fmt.Errorf("create markdown.new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "ElBot-WebExtract/1.0")
	httpReq.Header.Set("Accept", "application/json")
	resp, err := t.httpClient(proxy).Do(httpReq)
	if err != nil {
		return cachedExtract{}, fmt.Errorf("markdown.new request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return cachedExtract{}, fmt.Errorf("read markdown.new response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cachedExtract{}, fmt.Errorf("markdown.new HTTP %d: %s", resp.StatusCode, truncateHTTPBody(respBody))
	}
	var data markdownNewResponse
	if err := json.Unmarshal(respBody, &data); err != nil {
		return cachedExtract{}, fmt.Errorf("parse markdown.new response: %w", err)
	}
	if !data.Success {
		return cachedExtract{}, fmt.Errorf("markdown.new extraction failed: %s", truncateHTTPBody(respBody))
	}
	if data.URL == "" {
		data.URL = targetURL
	}
	return cachedExtract{url: data.URL, title: data.Title, source: "markdownnew", content: data.Content}, nil
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

func truncateHTTPBody(body []byte) string {
	const max = 2048
	text := string(body)
	if len(text) <= max {
		return text
	}
	return text[:max] + "..."
}
