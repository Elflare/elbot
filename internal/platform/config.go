package platform

import (
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// DecodeConfig decodes a raw [platform.<name>] app.toml section into an adapter-owned config struct.
func DecodeConfig(raw map[string]any, out any) error {
	if out == nil {
		return fmt.Errorf("platform config output is nil")
	}
	if raw == nil {
		return nil
	}
	data, err := toml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal platform config: %w", err)
	}
	if err := toml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode platform config: %w", err)
	}
	return nil
}

// StripTriggerKeyword removes only the matched keyword prefix from text.
func StripTriggerKeyword(text string, keywords []string) (string, bool) {
	text = strings.TrimSpace(text)
	for _, keyword := range keywords {
		keyword = strings.TrimSpace(keyword)
		if keyword != "" && strings.HasPrefix(text, keyword) {
			return strings.TrimSpace(strings.TrimPrefix(text, keyword)), true
		}
	}
	return text, false
}
