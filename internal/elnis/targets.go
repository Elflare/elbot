package elnis

import (
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/config"
)

func (s *Service) resolveTargets(req Request) ([]Target, error) {
	resolved := []Target{}
	for _, target := range req.Targets {
		if target.Platform == "all" {
			for _, platformName := range s.enabledTargetPlatforms() {
				candidate := Target{Platform: platformName}
				if !s.targetDisabled(req.Elwisp.Name, candidate) {
					resolved = append(resolved, candidate)
				}
			}
			continue
		}
		if !s.targetDisabled(req.Elwisp.Name, target) {
			resolved = append(resolved, target)
		}
	}
	resolved = uniqueTargets(resolved)
	if len(resolved) == 0 && req.Mode == ModeDirect {
		return nil, fmt.Errorf("no allowed delivery targets")
	}
	return resolved, nil
}

func (s *Service) enabledTargetPlatforms() []string {
	return s.enabledPlatforms
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
