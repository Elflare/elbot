// Package httpclient handles transport without owning authentication or API payloads.
package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"elbot/internal/llm"
)

type Options struct {
	FirstChunkTimeout time.Duration
	StreamIdleTimeout time.Duration
	MaxRetries        int
	RetryInitialDelay time.Duration
	OnRetry           func(context.Context, llm.RetryEvent)
	Proxy             string
	// HTTPClient optionally supplies a transport, for embedding and tests.
	HTTPClient *http.Client
}

func (o Options) withDefaults() Options {
	if o.FirstChunkTimeout <= 0 {
		o.FirstChunkTimeout = 180 * time.Second
	}
	if o.StreamIdleTimeout <= 0 {
		o.StreamIdleTimeout = 60 * time.Second
	}
	if o.MaxRetries <= 0 {
		o.MaxRetries = 3
	}
	if o.RetryInitialDelay <= 0 {
		o.RetryInitialDelay = 2 * time.Second
	}
	return o
}

type Client struct {
	http    *http.Client
	options Options
	onRetry func(context.Context, llm.RetryEvent)
}

func New(opts Options) (*Client, error) {
	opts = opts.withDefaults()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = opts.FirstChunkTimeout
	if opts.Proxy != "" {
		proxy, err := url.Parse(opts.Proxy)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy URL %q: %w", opts.Proxy, err)
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Transport: transport}
	}
	return &Client{http: client, options: opts, onRetry: opts.OnRetry}, nil
}

func (c *Client) SetRetryNotifier(f func(context.Context, llm.RetryEvent)) { c.onRetry = f }

// Do retries only the initial HTTP exchange, never a consumed response stream.
// The protocol client supplies the HTTP error decoder.
func (c *Client) Do(ctx context.Context, request func(context.Context) (*http.Request, error), decodeError func(*http.Response) error) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= c.options.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := request(ctx)
		if err != nil {
			return nil, err
		}
		resp, err := c.http.Do(req)
		if err == nil && !isRetryableStatus(resp.StatusCode) {
			return resp, nil
		}
		if err != nil {
			lastErr = fmt.Errorf("http request: %w", err)
		} else {
			lastErr = decodeError(resp)
			_ = resp.Body.Close()
			if lastErr == nil {
				lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			}
		}
		if attempt == c.options.MaxRetries {
			return nil, lastErr
		}
		delay := retryDelay(c.options.RetryInitialDelay, attempt)
		if c.onRetry != nil {
			c.onRetry(ctx, llm.RetryEvent{Attempt: attempt + 1, MaxRetries: c.options.MaxRetries, Delay: delay, Err: lastErr})
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
