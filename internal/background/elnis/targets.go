// targets.go 裁决启用平台与禁用目标，解析事件目标并转换为投递目标。

package elnis

import (
	"elbot/internal/contextinfo"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"elbot/internal/config"
	"elbot/internal/delivery"
)

func toDeliveryTarget(t Target) delivery.Target {
	out := delivery.Target{Platform: t.Platform, Superadmins: t.Type == ""}
	out.ChatType = contextinfo.ChatType(t.Type)
	out.ChatID = t.ID
	return out
}

func (s *Service) resolveTargets(req Request) ([]Target, error) {
	resolved := []Target{}
	var rejected []string
	for _, target := range req.Targets {
		candidates := []Target{target}
		if target.Platform == "all" {
			candidates = nil
			if len(s.enabledPlatforms) == 0 {
				rejected = append(rejected, "all: no enabled platforms")
			}
			for _, platformName := range s.enabledPlatforms {
				candidates = append(candidates, Target{Platform: platformName})
			}
		}
		for _, candidate := range candidates {
			if !slices.Contains(s.enabledPlatforms, candidate.Platform) {
				rejected = append(rejected, fmt.Sprintf("%s: platform is not enabled", candidate.Platform))
				continue
			}
			if s.targetDisabled(req.Elwisp.Name, candidate) {
				rejected = append(rejected, fmt.Sprintf("%s/%s/%s: target is disabled", candidate.Platform, candidate.Type, candidate.ID))
				continue
			}
			resolved = append(resolved, candidate)
		}
	}
	resolved = uniqueTargets(resolved)
	if len(resolved) == 0 && (req.Mode == ModeDirect || req.Mode == ModeLLM) {
		return nil, fmt.Errorf("no allowed delivery targets: %s", strings.Join(rejected, "; "))
	}
	return resolved, nil
}

func (s *Service) targetDisabled(elwispName string, target Target) bool {
	for _, disabled := range configTargetsToElnis(s.cfg.DeliveryDisabled.Targets) {
		if disabledTargetMatches(disabled, target) {
			return true
		}
	}
	if policy, ok := s.cfg.Elwisps[elwispName]; ok {
		for _, disabled := range configTargetsToElnis(policy.DisabledTargets) {
			if disabledTargetMatches(disabled, target) {
				return true
			}
		}
	}
	return false
}

func disabledTargetMatches(disabled, target Target) bool {
	if disabled.Platform != target.Platform {
		return false
	}
	if disabled.Type == "" && disabled.ID == "" {
		return true
	}
	return disabled.Type == target.Type && disabled.ID == target.ID
}

func configTargetsToElnis(targets []config.ElnisTargetConfig) []Target {
	out := make([]Target, 0, len(targets))
	for _, target := range targets {
		out = append(out, Target{Platform: strings.TrimSpace(target.Platform), Type: strings.TrimSpace(target.Type), ID: strings.TrimSpace(target.ID)})
	}
	return out
}

func decodeResolvedTargets(raw string) ([]Target, error) {
	var targets []Target
	if err := json.Unmarshal([]byte(raw), &targets); err != nil {
		return nil, err
	}
	return targets, nil
}
