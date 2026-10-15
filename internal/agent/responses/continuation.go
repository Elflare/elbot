package responses

import (
	"bytes"
	"elbot/internal/agent/dialogue"
	"encoding/json"
	"fmt"

	api "elbot/internal/llm/responses"
	"elbot/internal/storage"
)

// A branch's continuation is the suffix added after its response checkpoint.
// Keep it unresolved until pending inputs join it; earlier media belongs to the server chain.
func (c *Context) resolveContinuation(seed *storage.NativeSeed) (*nativeWindow, error) {
	var continuation, items []api.Item
	if err := json.Unmarshal([]byte(seed.ContinuationJSON), &continuation); err != nil {
		return nil, err
	}
	if len(continuation) == 0 {
		return &nativeWindow{}, nil
	}
	items, err := decodeSeedInputs(seed)
	if err != nil {
		return nil, err
	}
	start := len(items) - len(continuation)
	if start < 0 {
		return nil, fmt.Errorf("原生分支续接材料缺失")
	}
	for i, item := range continuation {
		if !bytes.Equal(item.Raw, items[start+i].Raw) {
			return nil, fmt.Errorf("原生分支续接材料与窗口不匹配")
		}
	}
	materials, err := dialogue.DecodeMaterials(seed.MaterialsJSON)
	if err != nil {
		return nil, err
	}
	w := &nativeWindow{Items: continuation}
	for _, m := range materials {
		if m.Index >= start {
			m.Index -= start
			w.Materials = append(w.Materials, m)
		}
	}
	return w, nil
}
