// 组织提示词名称数据及其纯转换。
package share

import (
	"context"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type PromptNames struct {
	Tools  []string
	Skills []string
}

func PromptNamesFromInfos(infos []Info) PromptNames {
	names := PromptNames{}
	for _, info := range infos {
		if info.Name == "discover_tool" || info.Hidden {
			continue
		}
		switch info.Source {
		case SourceSkillAgent, SourceSkillGo:
			names.Skills = append(names.Skills, info.Name)
		default:
			names.Tools = append(names.Tools, info.Name)
		}
	}
	return names
}

type SchemaProvider struct {
	Registry   *Registry
	Permission *security.Permission
}

func (p SchemaProvider) Schemas(ctx context.Context, mode string, session *storage.Session, scope session.Scope) ([]llm.ToolSchema, error) {
	_ = session
	_ = scope
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if mode != storage.SessionModeWork || p.Registry == nil {
		return nil, nil
	}
	return p.Registry.Schemas(), nil
}

func (p SchemaProvider) ToolNames(ctx context.Context, mode string, session *storage.Session, scope session.Scope) (PromptNames, error) {
	if err := ctx.Err(); err != nil {
		return PromptNames{}, err
	}
	if mode != storage.SessionModeWork || p.Registry == nil {
		return PromptNames{}, nil
	}
	actor := actorFromScope(ctx, scope)
	return PromptNamesFromInfos(p.allowedInfos(actor)), nil
}

func (p SchemaProvider) allowedInfos(actor contextinfo.Actor) []Info {
	permission := p.Permission
	if permission == nil {
		permission = security.DefaultPermission()
	}
	infos := p.Registry.List()
	out := make([]Info, 0, len(infos))
	for _, info := range infos {
		if CanAccessTool(actor, permission, info) || info.Name == "discover_tool" {
			out = append(out, info)
		}
	}
	return out
}

func actorFromScope(ctx context.Context, scope session.Scope) contextinfo.Actor {
	if actor, ok := contextinfo.ActorFromContext(ctx); ok {
		return actor
	}
	if permission, ok := security.PermissionFromContext(ctx); ok && permission != nil {
		return permission.Actor(scope.ActorID, scope.Platform, scope.ActorID, "")
	}
	return contextinfo.Actor{ID: scope.ActorID, Platform: scope.Platform, PlatformUserID: scope.ActorID, Role: contextinfo.RoleUser}
}
