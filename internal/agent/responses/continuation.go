package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	api "elbot/internal/llm/responses"
	"elbot/internal/storage"
)

// A branch's continuation is the suffix added after its response checkpoint.
// Resolve only that suffix; earlier media already belongs to the server chain.
func (c *Context) resolveContinuation(ctx context.Context, seed *storage.NativeSeed) ([]api.Item, func(), error) {
	noop := func() {}
	var continuation, items []api.Item
	if err := json.Unmarshal([]byte(seed.ContinuationJSON), &continuation); err != nil {
		return nil, noop, err
	}
	if len(continuation) == 0 {
		return nil, noop, nil
	}
	items, err := decodeSeedInputs(seed)
	if err != nil {
		return nil, noop, err
	}
	start := len(items) - len(continuation)
	if start < 0 {
		return nil, noop, fmt.Errorf("原生分支续接材料缺失")
	}
	for i, item := range continuation {
		if !bytes.Equal(item.Raw, items[start+i].Raw) {
			return nil, noop, fmt.Errorf("原生分支续接材料与窗口不匹配")
		}
	}
	var materials []material
	if err := json.Unmarshal([]byte(seed.MaterialsJSON), &materials); err != nil {
		return nil, noop, err
	}
	w := &nativeWindow{Items: continuation}
	for _, m := range materials {
		if m.ItemIndex >= start {
			m.ItemIndex -= start
			w.Materials = append(w.Materials, m)
		}
	}
	return c.Resolve(ctx, w)
}
