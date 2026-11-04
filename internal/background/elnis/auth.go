package elnis

import "elbot/internal/tool"

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/elvena"
	"elbot/internal/security"
)

func (s *Service) authenticate(token string) (string, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	for name, value := range s.tokens {
		if token == value {
			return name, true
		}
	}
	return "", false
}

func (s *Service) authorizeElwisp(event Event) error {
	policy, ok := s.cfg.Elwisps[event.Request.Elwisp.Name]
	if !ok {
		return nil
	}
	if policy.Enabled != nil && !*policy.Enabled {
		return fmt.Errorf("elwisp %q is disabled", event.Request.Elwisp.Name)
	}
	allowedTokens := trimStrings(policy.AllowedTokens)
	if len(allowedTokens) == 0 {
		return nil
	}
	if event.Origin.Kind != elvena.OriginHTTPToken {
		return nil
	}
	for _, token := range allowedTokens {
		if token == event.Origin.Name {
			return nil
		}
	}
	return fmt.Errorf("token %q is not allowed for elwisp %q", event.Origin.Name, event.Request.Elwisp.Name)
}

func (s *Service) authorizeInternalTools(ctx context.Context, event Event) error {
	allowed := s.allowedInternalTools(event.Request.Elwisp.Name)
	ctx = contextinfo.WithActor(ctx, elnisActor(event))
	selections, err := s.toolPreloader.BackgroundSelections(ctx, event.Request.ToolListNames)
	if err != nil {
		return err
	}
	for _, selection := range selections {
		for _, name := range selection.Names {
			if !allowed[name] {
				return fmt.Errorf("tool %q is not allowed for elwisp %q", name, event.Request.Elwisp.Name)
			}
		}
	}
	return nil
}

func (s *Service) allowedInternalTools(elwispName string) map[string]bool {
	allowedTools := s.cfg.AllowedTools
	if policy, ok := s.cfg.Elwisps[elwispName]; ok && policy.AllowedTools != nil {
		allowedTools = policy.AllowedTools
	}
	return setFromStrings(allowedTools)
}

func (s *Service) authorizeExternalTools(event Event) error {
	disabled := map[string]bool{}
	if policy, ok := s.cfg.Elwisps[event.Request.Elwisp.Name]; ok {
		disabled = setFromStrings(policy.DisabledExternalTools)
	}
	for _, declared := range event.Request.Tools {
		name := strings.TrimSpace(declared.Name)
		if disabled[name] {
			return fmt.Errorf("external tool %q is disabled for elwisp %q", name, event.Request.Elwisp.Name)
		}
	}
	return nil
}

var elwispToolNameReplacer = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func (s *Service) elwispTools(event Event) []tool.ELwispToolDeclaration {
	tools := append([]tool.ELwispToolDeclaration(nil), event.Request.Tools...)
	for i := range tools {
		tools[i].Risk = security.RiskLow
		tools[i].Name = strings.TrimSpace(tools[i].Name)
		tools[i].Endpoint = strings.TrimSpace(tools[i].Endpoint)
		tools[i].CanonicalName = elwispToolName(event.Request.Elwisp.Name, tools[i].Name)
		tools[i].EventKey = event.EventKey
	}
	return tools
}

func elwispToolName(elwispName, name string) string {
	elwispName = elwispToolNameReplacer.ReplaceAllString(strings.TrimSpace(elwispName), "_")
	name = elwispToolNameReplacer.ReplaceAllString(strings.TrimSpace(name), "_")
	return fmt.Sprintf("elwisp_%s_%s", elwispName, name)
}
