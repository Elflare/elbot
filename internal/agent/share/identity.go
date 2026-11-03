package share

import (
	"context"

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
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	if facts.Mode == "background" {
		return session.Scope{Owner: actor.ActorIdentity, Platform: actor.Platform, TaskType: facts.TaskType, TaskID: facts.TaskID}
	}
	return session.Scope{
		ChatType: info.Source.ChatType,
		Owner:    actor.ActorIdentity,

		Platform: info.Source.Platform,
		ChatID:   info.Source.ChatID,
		IsCLI:    info.Source.Platform == r.cliPlatformName && actor.Role == contextinfo.RoleSuperadmin,
	}
}

func (r *IdentityResolver) Actor(ctx context.Context) contextinfo.Actor {
	if actor, ok := contextinfo.ActorFromContext(ctx); ok {
		return actor
	}
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok {
		return contextinfo.Actor{}
	}
	actor := r.permission.Actor(info.Source.Platform, info.Identity.ID, info.Identity.Name)
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

// ExecutionMode 只供 Agent 内部编排消费已建立的执行模式。
func ExecutionMode(ctx context.Context) string {
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	return facts.Mode
}
