package elnis

import (
	"path/filepath"
	"sort"
	"strings"

	"elbot/internal/contextinfo"
)

func isElnisModelSlot(slot string) bool {
	switch strings.TrimSpace(slot) {
	case "elwisp1", "elwisp2", "elwisp3":
		return true
	default:
		return false
	}
}

func elnisActor(event Event) contextinfo.Actor {
	return contextinfo.Actor{ActorIdentity: contextinfo.ActorIdentity{ID: event.Request.Elwisp.Name}, Name: event.Request.Elwisp.Name, Role: contextinfo.RoleSuperadmin}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
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
