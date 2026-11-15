// Package httpclient handles transport without owning authentication or API payloads.
package httpclient

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"elbot/internal/llm"
)

type Options struct {
	// FirstChunkTimeout is supplied by configuration and bounds headers and the first stream data.
	FirstChunkTimeout time.Duration
	// StreamIdleTimeout bounds the wait between stream data after the first chunk.
	StreamIdleTimeout time.Duration
	Proxy             string
	// HTTPClient optionally supplies a transport, for embedding and tests.
	HTTPClient *http.Client
}

type Client struct {
	http    *http.Client
	options Options
}

func New(opts Options) (*Client, error) {
	client := opts.HTTPClient
	if client == nil {
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
		client = &http.Client{Transport: transport}
	}
	return &Client{http: client, options: opts}, nil
}

// Do performs one HTTP exchange and marks failures at their source.
// Non-200 bodies are decoded and closed here; the caller owns successful bodies.
func (c *Client) Do(req *http.Request, decodeError func(*http.Response) error) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, llm.MapTransportError(err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, decodeError(resp)
	}
	return resp, nil
}
