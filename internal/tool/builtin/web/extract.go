// 定义网页提取工具，准备调用并组织分页结果。
package web

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"elbot/internal/processenv"
)

const (
	defaultExtractChars   = 8000
	maxExtractChars       = 16000
	defaultExtractTimeout = 15 * time.Second
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
	Method         string `json:"method"`
	ForceRefresh   bool   `json:"force_refresh"`
}

type webExtractData struct {
	URL           string
	Title         string
	Source        string
	TotalChars    int
	Offset        int
	ReturnedChars int
	Cached        bool
	Content       string
}

func NewWebExtractTool(environment processenv.Environment) *WebExtractTool {
	return &WebExtractTool{endpoint: defaultExtractEndpoint, cache: newExtractCache(), processEnv: environment}
}

func (t *WebExtractTool) Definition() toolshare.ToolDefinition {
	return webExtractBuilder().Build(t.PrepareCall)
}

func webExtractBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("web_extract").
		Description("提取网页正文；同URL+offset→续读。").
		Risk(toolshare.RiskLow).
		Tags("web").
		DependsOn("web_search").
		String("url", "网页URL。", toolshare.Required()).
		Integer("offset", "Unicode字符偏移，0-based；默认0，越界截到正文边界。").
		Integer("max_chars", "返回字符上限；≤0/省略→8000，最大16000。").
		String("remove_selector", "仅Jina；覆盖默认移除项的CSS选择器，如header,.class,#id。").
		Integer("timeout_ms", "请求超时，ms；≤0/省略→15000。").
		String("proxy", "省略→配置/系统代理；disabled→直连；URL→指定代理。").
		String("method", "默认jina；direct：快速读取静态页；jina：清理杂项、提取正文；markdownnew：保留整页结构和链接。", toolshare.Enum("direct", "jina", "markdownnew")).
		Boolean("force_refresh", "true→绕过缓存重新获取；默认false。")
}

func (t *WebExtractTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (*toolshare.PreparedCall, error) {
	var args webExtractArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse web_extract arguments: %w", err)
		}
	}
	url := strings.TrimSpace(args.URL)
	if url == "" {
		return nil, fmt.Errorf("url is required")
	}
	proxy, err := resolveExtractProxy(t.processEnv, args.Proxy)
	if err != nil {
		return nil, err
	}
	cache := t.cache
	method := strings.TrimSpace(args.Method)
	if method == "" {
		method = "jina"
	}
	apiKey := ""
	selector := ""
	switch method {
	case "direct", "markdownnew":
	case "jina":
		selector = strings.TrimSpace(args.RemoveSelector)
		if selector == "" {
			selector = defaultRemoveSelector
		}
		apiKey, _ = t.processEnv.Lookup(jinaAPIKeyEnv)
		if strings.TrimSpace(apiKey) == "" {
			return nil, fmt.Errorf("%s is required for method=jina; set it in environment or config .env", jinaAPIKeyEnv)
		}
	default:
		return nil, fmt.Errorf("invalid web_extract method: %s; expected direct, jina, or markdownnew", method)
	}
	key := extractCacheKey(method, url, selector, proxy)
	timeout := defaultExtractTimeout
	if args.TimeoutMS > 0 {
		timeout = time.Duration(args.TimeoutMS) * time.Millisecond
	}
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, []toolshare.ConfirmationDetailBlock{toolshare.TextConfirmationDetail(fmt.Sprintf("提取网页：%s", url))}, func(ctx context.Context) (*toolshare.Result, string, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		entry, cached, err := cache.getOrFetch(ctx, key, args.ForceRefresh, func(fetchCtx context.Context) (cachedExtract, error) {
			switch method {
			case "jina":
				return t.fetchJina(fetchCtx, apiKey, url, selector, proxy)
			case "markdownnew":
				return t.fetchMarkdownNew(fetchCtx, url, proxy)
			default:
				return t.fetchDirect(fetchCtx, url, proxy)
			}
		})
		if err != nil {
			return nil, fmt.Sprintf("url=%s source=%s status=failed", url, method), err
		}
		data := sliceExtract(entry, args.Offset, args.MaxChars, cached)
		summary := fmt.Sprintf("url=%s range=%d-%d/%d cached=%t source=%s title=%s", data.URL, data.Offset, data.Offset+data.ReturnedChars, data.TotalChars, data.Cached, data.Source, strings.Join(strings.Fields(data.Title), " "))
		return &toolshare.Result{Summary: summary, Content: data.Content}, summary, nil
	}), nil
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
	return webExtractData{
		URL:           entry.url,
		Title:         entry.title,
		Source:        entry.source,
		TotalChars:    total,
		Offset:        offset,
		ReturnedChars: returned,
		Cached:        cached,
		Content:       content,
	}
}
