package elnis

import "elbot/internal/tool"

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/elvena"
	"elbot/internal/operationresult"
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

func (s *Service) authorizeElwisp(event Event) operationresult.Result[struct{}] {
	policy, ok := s.cfg.Elwisps[event.Request.Elwisp.Name]
	if !ok {
		return operationresult.Result[struct{}]{Status: operationresult.Success}
	}
	if policy.Enabled != nil && !*policy.Enabled {
		return operationresult.Result[struct{}]{Status: operationresult.Rejected, Message: fmt.Sprintf("elwisp %q is disabled", event.Request.Elwisp.Name)}
	}
	allowedTokens := trimStrings(policy.AllowedTokens)
	if len(allowedTokens) == 0 {
		return operationresult.Result[struct{}]{Status: operationresult.Success}
	}
	if event.Origin.Kind != elvena.OriginHTTPToken {
		return operationresult.Result[struct{}]{Status: operationresult.Success}
	}
	for _, token := range allowedTokens {
		if token == event.Origin.Name {
			return operationresult.Result[struct{}]{Status: operationresult.Success}
		}
	}
	return operationresult.Result[struct{}]{Status: operationresult.Rejected, Message: fmt.Sprintf("token %q is not allowed for elwisp %q", event.Origin.Name, event.Request.Elwisp.Name)}
}

func (s *Service) authorizeInternalTools(ctx context.Context, event Event) (operationresult.Result[struct{}], error) {
	allowed := s.allowedInternalTools(event.Request.Elwisp.Name)
	ctx = contextinfo.WithActor(ctx, elnisActor(event))
	selections, err := s.toolPreloader.BackgroundSelections(ctx, event.Request.ToolListNames)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	for _, selection := range selections {
		for _, name := range selection.Names {
			if !allowed[name] {
				return operationresult.Result[struct{}]{Status: operationresult.Rejected, Message: fmt.Sprintf("tool %q is not allowed for elwisp %q", name, event.Request.Elwisp.Name)}, nil
			}
		}
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func (s *Service) allowedInternalTools(elwispName string) map[string]bool {
	allowedTools := s.cfg.AllowedTools
	if policy, ok := s.cfg.Elwisps[elwispName]; ok && policy.AllowedTools != nil {
		allowedTools = policy.AllowedTools
	}
	return setFromStrings(allowedTools)
}

func (s *Service) authorizeExternalTools(event Event) operationresult.Result[struct{}] {
	disabled := map[string]bool{}
	if policy, ok := s.cfg.Elwisps[event.Request.Elwisp.Name]; ok {
		disabled = setFromStrings(policy.DisabledExternalTools)
	}
	for _, declared := range event.Request.Tools {
		name := strings.TrimSpace(declared.Name)
		if disabled[name] {
			return operationresult.Result[struct{}]{Status: operationresult.Rejected, Message: fmt.Sprintf("external tool %q is disabled for elwisp %q", name, event.Request.Elwisp.Name)}
		}
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}
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
