package elnis

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"elbot/internal/elvena"
	"elbot/internal/toolrun"
)

var elwispNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (s *Service) prepareEvent(origin elvena.Origin, req Request) (Event, error) {
	req.Version = strings.TrimSpace(req.Version)
	req.Elwisp.Name = strings.TrimSpace(req.Elwisp.Name)
	req.Source = strings.TrimSpace(req.Source)
	req.ID = strings.TrimSpace(req.ID)
	req.Mode = strings.TrimSpace(req.Mode)
	req.Format = strings.TrimSpace(req.Format)
	req.ModelSlot = strings.TrimSpace(req.ModelSlot)
	req.Content = strings.TrimSpace(req.Content)
	if req.Version != elvena.VersionV2 && req.Version != elvena.VersionV3 {
		return Event{}, fmt.Errorf("unsupported ELvena version %q", req.Version)
	}
	if req.Elwisp.Name == "" {
		return Event{}, fmt.Errorf("elwisp.name is required")
	}
	if !elwispNamePattern.MatchString(req.Elwisp.Name) {
		return Event{}, fmt.Errorf("elwisp.name must use only letters, digits, _ or -")
	}
	if req.Source == "" {
		return Event{}, fmt.Errorf("source is required")
	}
	if req.ID == "" {
		return Event{}, fmt.Errorf("id is required")
	}
	if req.Content == "" && len(req.Segments) == 0 && len(req.Calls) == 0 {
		return Event{}, fmt.Errorf("content, segments or calls is required")
	}
	if req.Format == "" {
		req.Format = "text"
	}
	if req.Format != "text" && req.Format != "elyph" {
		return Event{}, fmt.Errorf("unsupported format %q", req.Format)
	}
	if req.Mode != ModeRecord && req.Mode != ModeDirect && req.Mode != ModeLLM {
		return Event{}, fmt.Errorf("unsupported mode %q", req.Mode)
	}
	if req.Mode == ModeLLM && req.Content == "" {
		return Event{}, fmt.Errorf("content is required for llm mode")
	}
	if req.ModelSlot != "" && !isElnisModelSlot(req.ModelSlot) {
		return Event{}, fmt.Errorf("unsupported model_slot %q", req.ModelSlot)
	}
	createdAt := time.Now()
	if strings.TrimSpace(req.CreatedAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(req.CreatedAt))
		if err != nil {
			return Event{}, fmt.Errorf("created_at must be RFC3339: %w", err)
		}
		createdAt = parsed
	}
	tagsJSON, err := json.Marshal(trimStrings(req.Elwisp.Tags))
	if err != nil {
		return Event{}, err
	}
	targets, err := normalizeTargets(req.Targets)
	if err != nil {
		return Event{}, err
	}
	req.Targets = targets
	requestedTargets, err := json.Marshal(targets)
	if err != nil {
		return Event{}, err
	}
	resolved, err := s.resolveTargets(req)
	if err != nil {
		return Event{}, err
	}
	resolvedTargets, err := json.Marshal(resolved)
	if err != nil {
		return Event{}, err
	}
	toolDeclarations, err := normalizedToolDeclarations(req.Tools)
	if err != nil {
		return Event{}, err
	}
	return Event{
		Request:          req,
		Origin:           origin,
		EventKey:         req.Elwisp.Name + "/" + req.Source + "/" + req.ID,
		ContentHash:      contentHash(req),
		ToolDeclarations: toolDeclarations,
		ToolHash:         hashText(toolDeclarations),
		TagsJSON:         string(tagsJSON),
		RequestedTargets: string(requestedTargets),
		ResolvedTargets:  string(resolvedTargets),
		CreatedAt:        createdAt,
		ReceivedAt:       time.Now(),
	}, nil
}

func normalizedToolDeclarations(tools []toolrun.ELwispToolDeclaration) (string, error) {
	if len(tools) == 0 {
		return "", nil
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func normalizeTargets(targets []Target) ([]Target, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("targets is required")
	}
	out := make([]Target, 0, len(targets))
	for _, target := range targets {
		target.Platform = strings.TrimSpace(target.Platform)
		target.Type = strings.TrimSpace(target.Type)
		target.ID = strings.TrimSpace(target.ID)
		if target.Platform == "" {
			return nil, fmt.Errorf("targets.platform is required")
		}
		if target.Platform == "all" {
			if target.Type != "" || target.ID != "" {
				return nil, fmt.Errorf("targets with platform all cannot set type or id")
			}
			out = append(out, target)
			continue
		}
		switch target.Type {
		case "":
			if target.ID != "" {
				return nil, fmt.Errorf("targets.id requires type")
			}
		case "private", "group":
			if target.ID == "" {
				return nil, fmt.Errorf("targets.id is required for %s target", target.Type)
			}
		default:
			return nil, fmt.Errorf("unsupported target type %q", target.Type)
		}
		out = append(out, target)
	}
	return uniqueTargets(out), nil
}

func uniqueTargets(targets []Target) []Target {
	seen := map[string]bool{}
	out := []Target{}
	for _, target := range targets {
		key := target.Platform + "\x00" + target.Type + "\x00" + target.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, target)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Platform != out[j].Platform {
			return out[i].Platform < out[j].Platform
		}
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].ID < out[j].ID
	})
	return out
}
