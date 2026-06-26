package resident_memory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/memory/resident"
	"elbot/internal/session"
)

const DefaultPriority = 100

type Options struct {
	Store    *resident.Store
	Priority int
}

// Module injects resident memory into LLM requests through the hook layer.
type Module struct {
	Store    *resident.Store
	Priority int
}

func NewModule(opts Options) Module {
	return Module{Store: opts.Store, Priority: opts.Priority}
}

func (m Module) RegisterHooks(registrar hook.Registrar) error {
	if registrar == nil || m.Store == nil {
		return nil
	}
	priority := m.Priority
	if priority == 0 {
		priority = DefaultPriority
	}
	if err := registrar.Register(hook.Registration{
		Point:    hook.PointLLMTurnPrepared,
		Priority: priority,
		Name:     "plugins.resident_memory",
		Match:    hook.Always(),
		Detail:   "每 turn 注入当前 platform + actor 的常驻记忆和临时用户名",
		Handler:  hook.HandlerFunc(m.inject),
	}); err != nil {
		return err
	}
	return nil
}

func (m Module) inject(ctx context.Context, event hook.Event) (hook.Event, error) {
	if event.Point != hook.PointLLMTurnPrepared || m.Store == nil {
		return event, nil
	}
	memory, err := m.Store.Read(ctx, scopeFromEvent(event))
	content := ""
	if errors.Is(err, resident.ErrNotFound) {
		content = defaultUserNameMemory(event)
	} else if err != nil {
		return event, err
	} else {
		content = memory.Text()
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return event, nil
	}
	event.LLM.Messages = injectSystemContent(event.LLM.Messages, content)
	return event, nil
}

func scopeFromEvent(event hook.Event) session.Scope {
	return session.Scope{Platform: event.Platform.Name, ActorID: event.Actor.ID}
}

func defaultUserNameMemory(event hook.Event) string {
	name := strings.TrimSpace(event.Actor.DisplayName)
	if name == "" && event.Platform.Name == "cli" {
		name = "管理员"
	}
	if name == "" {
		return ""
	}
	return fmt.Sprintf("用户名字：%s。", name)
}

func injectSystemContent(messages []llm.LLMMessage, content string) []llm.LLMMessage {
	return llm.AppendSystemSegmentText(messages, content)
}
