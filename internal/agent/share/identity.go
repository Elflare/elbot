package share

import (
	"context"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/session"
)

// IdentityResolver 按请求携带的来源解析身份，不为无来源调用补造用户或会话。
type IdentityResolver struct {
	cliPlatformName string // 根部注入的 CLI 平台身份。
	permission      interface {
		Actor(string, string, string) contextinfo.Actor
	} // 根部仅注入身份解析能力。
}

func (r *IdentityResolver) Scope(ctx context.Context) session.Scope {
	actor := r.Actor(ctx)
	info, _ := contextinfo.ConversationFromContext(ctx)
	return session.Scope{
		ConversationKind: info.Source.ConversationKind,
		ActorID:          actor.ID,
		PlatformUserID:   actor.PlatformUserID,
		Platform:         info.Source.Platform,
		PlatformScopeID:  info.Source.ScopeID,
		IsCLI:            info.Source.Platform == r.cliPlatformName && actor.Role == contextinfo.RoleSuperadmin,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (r *IdentityResolver) Actor(ctx context.Context) contextinfo.Actor {
	if actor, ok := contextinfo.ActorFromContext(ctx); ok {
		return actor
	}
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || info.Identity.PlatformUserID == "" {
		return contextinfo.Actor{}
	}
	actor := r.permission.Actor(info.Source.Platform, info.Identity.PlatformUserID, info.Identity.DisplayName)
	actor.Nickname, actor.GroupCard = info.Identity.Nickname, info.Identity.GroupCard
	if msg, ok := MessageContextFrom(ctx); ok {
		actor.GroupRole = msg.GroupRole
	}
	return actor
}

func (r *IdentityResolver) IsCLI(ctx context.Context) bool {
	info, ok := contextinfo.ConversationFromContext(ctx)
	return ok && info.Source.Platform == r.cliPlatformName
}

// IdentityResolverDeps declares only the capabilities consumed by IdentityResolver.
type IdentityResolverDeps struct {
	CLIPlatformName string // CLI 身份比较所需的固定平台名。
	Permission      interface {
		Actor(string, string, string) contextinfo.Actor
	} // 身份解析不取得工具授权或确认策略。
}

// NewIdentityResolver initializes IdentityResolver without installing cross-service callbacks.
func NewIdentityResolver(deps IdentityResolverDeps) *IdentityResolver {
	return &IdentityResolver{
		cliPlatformName: deps.CLIPlatformName,
		permission:      deps.Permission,
	}
}
