// 实现网页搜索工具，组织搜索请求和结果。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"elbot/internal/llm"
	"elbot/internal/processenv"
)

const (
	tavilyAPIKeyEnv       = "TAVILY_API_KEY"
	defaultSearchResults  = 5
	defaultSearchDepth    = "basic"
	defaultSearchEndpoint = "https://api.tavily.com/search"
)

type WebSearchTool struct {
	client     *http.Client
	endpoint   string
	processEnv processenv.Environment
}

type webSearchArgs struct {
	Query          string   `json:"query"`
	SearchDepth    string   `json:"search_depth"`
	MaxResults     int      `json:"max_results"`
	Topic          string   `json:"topic"`
	TimeRange      string   `json:"time_range"`
	IncludeDomains []string `json:"include_domains"`
	ExcludeDomains []string `json:"exclude_domains"`
	Country        string   `json:"country"`
	ExactMatch     bool     `json:"exact_match"`
}

type tavilyRequest struct {
	Query                    string   `json:"query"`
	SearchDepth              string   `json:"search_depth"`
	MaxResults               int      `json:"max_results"`
	Topic                    string   `json:"topic,omitempty"`
	TimeRange                string   `json:"time_range,omitempty"`
	IncludeAnswer            bool     `json:"include_answer"`
	IncludeRawContent        bool     `json:"include_raw_content"`
	IncludeImages            bool     `json:"include_images"`
	IncludeImageDescriptions bool     `json:"include_image_descriptions"`
	IncludeFavicon           bool     `json:"include_favicon"`
	IncludeUsage             bool     `json:"include_usage"`
	AutoParameters           bool     `json:"auto_parameters"`
	IncludeDomains           []string `json:"include_domains,omitempty"`
	ExcludeDomains           []string `json:"exclude_domains,omitempty"`
	Country                  string   `json:"country,omitempty"`
	ExactMatch               bool     `json:"exact_match,omitempty"`
}

type tavilyResponse struct {
	Query        string         `json:"query"`
	Answer       string         `json:"answer"`
	Results      []tavilyResult `json:"results"`
	ResponseTime float64        `json:"response_time"`
	RequestID    string         `json:"request_id"`
}

type tavilyResult struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

func NewWebSearchTool(environment processenv.Environment) WebSearchTool {
	return WebSearchTool{client: &http.Client{Timeout: 15 * time.Second}, endpoint: defaultSearchEndpoint, processEnv: environment}
}

func (WebSearchTool) Name() string {
	return "web_search"
}

func (t WebSearchTool) Info() toolshare.Info {
	return webSearchBuilder().BuildInfo()
}

func (t WebSearchTool) Schema() llm.ToolSchema {
	return webSearchBuilder().BuildSchema()
}

func webSearchBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("web_search").
		Description("网页搜索→答案+来源链接+摘要；正文→web_extract。").
		Risk(toolshare.RiskLow).
		Tags("web").
		DependsOn("web_extract").
		String("query", "搜索词。", toolshare.Required()).
		Integer("max_results", "≤0/省略→5，上限20。").
		String("search_depth", "搜索深度，默认basic。", toolshare.Enum("basic", "fast", "ultra-fast", "advanced")).
		String("topic", "类别，默认general。", toolshare.Enum("general", "news", "finance")).
		String("time_range", "最近时间范围；d/w/m/y为对应缩写。", toolshare.Enum("day", "week", "month", "year", "d", "w", "m", "y")).
		StringArray("include_domains", "仅这些域名。").
		StringArray("exclude_domains", "排除这些域名。").
		String("country", "仅general；区域偏好，如china、united states。").
		Boolean("exact_match", "true→精确匹配query中的引号短语。")
}

func (t WebSearchTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	var args webSearchArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse web_search arguments: %w", err)
		}
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	apiKey, ok := t.processEnv.Lookup(tavilyAPIKeyEnv)
	if !ok || strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("%s is required; set it in environment or config .env", tavilyAPIKeyEnv)
	}
	payload := normalizeTavilyRequest(args, query)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal Tavily request: %w", err)
	}

	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, fmt.Sprintf("搜索：%s", query), func(ctx context.Context) (_ *toolshare.Result, logContent string, _ error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.searchEndpoint(), bytes.NewReader(body))
		if err != nil {
			callErr := fmt.Errorf("create Tavily request: %w", err)
			return nil, callErr.Error(), callErr
		}
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		httpReq.Header.Set("Content-Type", "application/json")

		client := t.httpClient()
		resp, err := client.Do(httpReq)
		if err != nil {
			callErr := fmt.Errorf("Tavily request: %w", err)
			return nil, callErr.Error(), callErr
		}
		defer resp.Body.Close()
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			callErr := fmt.Errorf("read Tavily response: %w", err)
			return nil, callErr.Error(), callErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			callErr := fmt.Errorf("Tavily HTTP %d: %s", resp.StatusCode, truncateHTTPBody(respBody))
			return nil, callErr.Error(), callErr
		}
		var data tavilyResponse
		if err := json.Unmarshal(respBody, &data); err != nil {
			callErr := fmt.Errorf("parse Tavily response: %w", err)
			return nil, callErr.Error(), callErr
		}
		result := &toolshare.Result{Content: formatTavilyContent(data)}
		logContent = fmt.Sprintf("results=%d", len(data.Results))
		for _, item := range data.Results {
			logContent += fmt.Sprintf("\n%s %s", item.Title, item.URL)
		}
		return result, logContent, nil
	}), nil
}

func normalizeTavilyRequest(args webSearchArgs, query string) tavilyRequest {
	maxResults := args.MaxResults
	if maxResults <= 0 {
		maxResults = defaultSearchResults
	}
	if maxResults > 20 {
		maxResults = 20
	}
	depth := strings.TrimSpace(args.SearchDepth)
	if depth == "" {
		depth = defaultSearchDepth
	}
	return tavilyRequest{
		Query:                    query,
		SearchDepth:              depth,
		MaxResults:               maxResults,
		Topic:                    strings.TrimSpace(args.Topic),
		TimeRange:                strings.TrimSpace(args.TimeRange),
		IncludeAnswer:            true,
		IncludeRawContent:        false,
		IncludeImages:            false,
		IncludeImageDescriptions: false,
		IncludeFavicon:           false,
		IncludeUsage:             false,
		AutoParameters:           false,
		IncludeDomains:           trimStringList(args.IncludeDomains),
		ExcludeDomains:           trimStringList(args.ExcludeDomains),
		Country:                  strings.TrimSpace(args.Country),
		ExactMatch:               args.ExactMatch,
	}
}

func formatTavilyContent(data tavilyResponse) string {
	var out strings.Builder
	if data.Answer != "" {
		out.WriteString("Answer: ")
		out.WriteString(data.Answer)
		out.WriteString("\n\n")
	}
	for i, result := range data.Results {
		if i > 0 {
			out.WriteString("\n")
		}
		out.WriteString(fmt.Sprintf("%d. %s\n", i+1, result.Title))
		out.WriteString(result.URL)
		if result.Content != "" {
			out.WriteString("\n")
			out.WriteString(result.Content)
		}
		out.WriteString("\n")
	}
	return strings.TrimSpace(out.String())
}

func (t WebSearchTool) httpClient() *http.Client {
	if t.client != nil {
		return t.client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (t WebSearchTool) searchEndpoint() string {
	if strings.TrimSpace(t.endpoint) != "" {
		return t.endpoint
	}
	return defaultSearchEndpoint
}

func trimStringList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func truncateHTTPBody(body []byte) string {
	const max = 2048
	text := string(body)
	if len(text) <= max {
		return text
	}
	return text[:max] + "..."
}
