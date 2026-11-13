// 缓存提取结果，共享进行中的请求并协调等待者取消。
package web

import (
	"context"

	"elbot/internal/operationresult"
	"sync"
)

const maxExtractCacheEntries = 8

type cachedExtract struct {
	url     string
	title   string
	source  string
	content string
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
	entry operationresult.Result[cachedExtract]
	err   error
}

func newExtractCache() *extractCache {
	return &extractCache{values: map[string]cachedExtract{}, inflight: map[string]*extractFlight{}}
}

func (c *extractCache) getOrFetch(ctx context.Context, key string, forceRefresh bool, fetch func(context.Context) (operationresult.Result[cachedExtract], error)) (operationresult.Result[cachedExtract], bool, error) {
	c.mu.Lock()
	if entry, ok := c.values[key]; ok && !forceRefresh {
		c.mu.Unlock()
		return operationresult.Result[cachedExtract]{Status: operationresult.Success, Data: entry}, true, nil
	}
	flight, exists := c.inflight[key]
	if !exists {
		fetchCtx, cancel := context.WithCancel(context.Background())
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
				if err == nil && entry.Status == operationresult.Success {
					c.setLocked(key, entry.Data)
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
		return operationresult.Result[cachedExtract]{}, false, ctx.Err()
	case <-flight.done:
		if err := ctx.Err(); err != nil {
			return operationresult.Result[cachedExtract]{}, false, err
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
	return source + "\x00" + targetURL + "\x00" + selector + "\x00" + proxy.cacheKey
}
