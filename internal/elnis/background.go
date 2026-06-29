package elnis

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"elbot/internal/security"
	"elbot/internal/storage"
)

func isElnisModelSlot(slot string) bool {
	switch strings.TrimSpace(slot) {
	case "elwisp1", "elwisp2", "elwisp3":
		return true
	default:
		return false
	}
}

func isElnisSessionMode(mode string) bool {
	switch strings.TrimSpace(mode) {
	case storage.SessionModeWork, storage.SessionModeChat:
		return true
	default:
		return false
	}
}

func elnisActor(event Event) security.Actor {
	id := security.ActorID("elnis", event.Origin.Label())
	return security.Actor{ID: id, Platform: "elnis", PlatformUserID: event.Origin.Label(), DisplayName: event.Request.Elwisp.Name, Role: security.RoleSuperadmin}
}

func firstPlatform(rawTargets string) string {
	var targets []Target
	if err := json.Unmarshal([]byte(rawTargets), &targets); err != nil {
		return ""
	}
	for _, target := range targets {
		if strings.TrimSpace(target.Platform) != "" {
			return target.Platform
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range trimStrings(values) {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func elnisSandboxSubdir(elwispName string) string {
	return filepath.ToSlash(filepath.Join("elnis", strings.TrimSpace(elwispName)))
}
