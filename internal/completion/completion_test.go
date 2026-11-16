package completion

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/tool"
)

type staticSource []Item

func (s staticSource) Complete(context.Context, Request) ([]Item, error) { return s, nil }

func TestServiceReturnsFirstNonEmptySource(t *testing.T) {
	svc := NewService(staticSource(nil), staticSource{{Text: "/model"}}, staticSource{{Text: "/models"}})
	got, _ := svc.Complete(context.Background(), Request{Text: "/m"})
	if len(got) != 1 || got[0].Text != "/model" {
		t.Fatalf("Complete = %#v", got)
	}
}

func TestRouterSourceCompletesCommands(t *testing.T) {
	router := command.NewRouter([]string{"/"})
	if err := router.Register(command.NewFunc(command.Info{Name: "model", Aliases: []string{"m"}, MinRole: contextinfo.RoleUser}, nil)); err != nil {
		t.Fatalf("register model: %v", err)
	}
	if err := router.Register(command.NewFunc(command.Info{Name: "models", MinRole: contextinfo.RoleUser}, nil)); err != nil {
		t.Fatalf("register models: %v", err)
	}
	items, _ := RouterSource{Router: router}.Complete(contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleUser}), Request{Text: "/m"})
	if len(items) != 3 || items[0].Text != "/model" || items[1].Text != "/m" || items[2].Text != "/models" {
		t.Fatalf("Complete = %#v", items)
	}
	for _, item := range items {
		if item.Kind != KindCommand {
			t.Fatalf("item kind = %q", item.Kind)
		}
	}
}

func TestRouterSourceFiltersCommandsByActor(t *testing.T) {
	router := command.NewRouter([]string{"/"})
	if err := router.Register(command.NewFunc(command.Info{Name: "public", Aliases: []string{"p"}, MinRole: contextinfo.RoleUser}, nil)); err != nil {
		t.Fatal(err)
	}
	if err := router.Register(command.NewFunc(command.Info{Name: "secret", Aliases: []string{"s"}}, nil)); err != nil {
		t.Fatal(err)
	}
	if err := router.Register(commandArgCompleter{}); err != nil {
		t.Fatal(err)
	}

	userSource := RouterSource{Router: router}
	userCtx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleUser})
	items, _ := userSource.Complete(userCtx, Request{Text: "/"})
	if len(items) != 2 || items[0].Text != "/public" || items[1].Text != "/p" {
		t.Fatalf("user command completions = %#v", items)
	}
	if got, _ := userSource.Complete(userCtx, Request{Text: "/s"}); len(got) != 0 {
		t.Fatalf("user private command completions = %#v", got)
	}
	if got, _ := userSource.Complete(userCtx, Request{Text: "/help mo", Cursor: len("/help mo")}); len(got) != 0 {
		t.Fatalf("user private argument completions = %#v", got)
	}

	adminSource := RouterSource{Router: router}
	adminCtx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	if got, _ := adminSource.Complete(adminCtx, Request{Text: "/s"}); len(got) != 2 || got[0].Text != "/secret" || got[1].Text != "/s" {
		t.Fatalf("admin private command completions = %#v", got)
	}
	if got, _ := adminSource.Complete(adminCtx, Request{Text: "/help mo", Cursor: len("/help mo")}); len(got) != 1 || got[0].Text != "model" {
		t.Fatalf("admin private argument completions = %#v", got)
	}
}
func TestToolDirectiveSourceCompletesOnlyPlainTools(t *testing.T) {
	source := ToolDirectiveSource{Context: func(ctx context.Context) (context.Context, error) {
		return withCompletionNames(ctx, []string{"web_extract", "web_search", "my_web", "shell"}, []string{"docx"}), nil
	}, Tools: completionCatalog{tools: []tool.Info{{Name: "web_extract"}, {Name: "web_search"}, {Name: "my_web"}, {Name: "shell"}}, skills: []tool.Info{{Name: "docx"}}, tags: []string{"web", "new_web"}, names: []string{"web_extract", "web_search"}}}

	items, _ := source.Complete(context.Background(), Request{Text: "查 @tool:we", Cursor: len("查 @tool:we")})
	if len(items) != 3 || items[0].Text != "@tool:web" || items[0].Label != "web <tag>" || items[0].Kind != KindToolTagDirective || items[1].Text != "@tool:web_extract" || items[2].Text != "@tool:web_search" || items[0].ReplaceStart != len("查 ") || items[0].ReplaceEnd != len("查 @tool:we") {
		t.Fatalf("Complete @tool prefix = %#v", items)
	}
	items, _ = source.Complete(context.Background(), Request{Text: "查 @t", Cursor: len("查 @t")})
	if len(items) != 1 || items[0].Text != "@t:" || items[0].ReplaceStart != len("查 ") || items[0].ReplaceEnd != len("查 @t") {
		t.Fatalf("Complete @t prefix = %#v", items)
	}
	items, _ = source.Complete(context.Background(), Request{Text: "@tool:shl", Cursor: len("@tool:shl")})
	if len(items) != 1 || items[0].Text != "@tool:shell" {
		t.Fatalf("fuzzy complete = %#v", items)
	}
	items, _ = source.Complete(context.Background(), Request{Text: "@t:shl", Cursor: len("@t:shl")})
	if len(items) != 1 || items[0].Text != "@t:shell" {
		t.Fatalf("short fuzzy complete = %#v", items)
	}
	items, _ = source.Complete(context.Background(), Request{Text: "@tool：shl", Cursor: len("@tool：shl")})
	if len(items) != 1 || items[0].Text != "@tool：shell" {
		t.Fatalf("full-width colon complete = %#v", items)
	}
	if got, _ := source.Complete(context.Background(), Request{Text: "@tool:d", Cursor: len("@tool:d")}); len(got) != 0 {
		t.Fatalf("skill should not complete: %#v", got)
	}
}

func TestToolDirectiveSourceCompletesSkillsSeparately(t *testing.T) {
	source := ToolDirectiveSource{Context: func(ctx context.Context) (context.Context, error) {
		return withCompletionNames(ctx, []string{"doc_tool"}, []string{"docx"}), nil
	}, Tools: completionCatalog{tools: []tool.Info{{Name: "doc_tool"}}, skills: []tool.Info{{Name: "docx"}}}}

	items, _ := source.Complete(context.Background(), Request{Text: "查 @skill:do", Cursor: len("查 @skill:do")})
	if len(items) != 1 || items[0].Text != "@skill:docx" || items[0].Kind != KindSkillDirective || items[0].ReplaceStart != len("查 ") || items[0].ReplaceEnd != len("查 @skill:do") {
		t.Fatalf("Complete @skill prefix = %#v", items)
	}
	items, _ = source.Complete(context.Background(), Request{Text: "查 @s", Cursor: len("查 @s")})
	if len(items) != 1 || items[0].Text != "@s:" {
		t.Fatalf("Complete @s prefix = %#v", items)
	}
	items, _ = source.Complete(context.Background(), Request{Text: "查 @s:do", Cursor: len("查 @s:do")})
	if len(items) != 1 || items[0].Text != "@s:docx" || items[0].Kind != KindSkillDirective {
		t.Fatalf("Complete @s prefix = %#v", items)
	}
	items, _ = source.Complete(context.Background(), Request{Text: "查 @skill：do", Cursor: len("查 @skill：do")})
	if len(items) != 1 || items[0].Text != "@skill：docx" || items[0].Kind != KindSkillDirective {
		t.Fatalf("Complete full-width skill prefix = %#v", items)
	}
	if got, _ := source.Complete(context.Background(), Request{Text: "@skill:doc_", Cursor: len("@skill:doc_")}); len(got) != 0 {
		t.Fatalf("plain tool should not complete as skill: %#v", got)
	}
}

func TestToolDirectiveSourceCompletesConfiguredTags(t *testing.T) {
	source := ToolDirectiveSource{Context: func(ctx context.Context) (context.Context, error) {
		return withCompletionNames(ctx, []string{"alpha"}, nil), nil
	}, Tools: completionCatalog{tools: []tool.Info{{Name: "alpha"}}, tags: []string{"agent"}, names: []string{"alpha"}}}

	items, _ := source.Complete(context.Background(), Request{Text: "@tool:ag", Cursor: len("@tool:ag")})
	if len(items) != 1 || items[0].Text != "@tool:agent" || items[0].Description != "1 item" {
		t.Fatalf("Complete configured tag = %#v", items)
	}
}

func TestRouterSourceCompletesCommandArgs(t *testing.T) {
	router := command.NewRouter([]string{"/"})
	if err := router.Register(commandArgCompleter{}); err != nil {
		t.Fatal(err)
	}
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	items, _ := RouterSource{Router: router}.Complete(ctx, Request{Text: "/help mo", Cursor: len("/help mo")})
	if len(items) != 1 || items[0].Text != "model" || items[0].ReplaceStart != len("/help ") || items[0].ReplaceEnd != len("/help mo") {
		t.Fatalf("Complete = %#v", items)
	}
}

type commandArgCompleter struct{}

func (commandArgCompleter) Info() command.Info { return command.Info{Name: "help"} }
func (commandArgCompleter) Handle(context.Context, command.Request) (command.Result, error) {
	return command.Result{}, nil
}
func (commandArgCompleter) Complete(_ context.Context, req command.CompletionRequest) []command.Completion {
	return []command.Completion{{Text: "model", Label: "model", Kind: "command_arg", ReplaceStart: len("/help "), ReplaceEnd: req.Cursor}}
}

// completionCatalog supplies query results; filtering policy belongs to Tool.
type completionCatalog struct {
	tools, skills []tool.Info
	tags, names   []string
	query         func(context.Context, tool.ToolQuery) ([]tool.ToolInfo, error)
}

func (c completionCatalog) QueryTools(ctx context.Context, q tool.ToolQuery) ([]tool.ToolInfo, error) {
	if c.query != nil {
		return c.query(ctx, q)
	}
	if q.Tag != "" {
		if q.Kind != "" {
			return nil, errors.New("tag query must include tools and skills")
		}
		var infos []tool.ToolInfo
		for _, name := range c.names {
			infos = append(infos, tool.ToolInfo{Definition: tool.Info{Name: name}})
		}
		return infos, nil
	}
	values := c.tools
	if q.Kind == "skill" {
		values = c.skills
	}
	var infos []tool.ToolInfo
	for _, info := range values {
		infos = append(infos, tool.ToolInfo{Definition: info})
	}
	return infos, nil
}
func (c completionCatalog) ToolTags(context.Context) ([]string, error) { return c.tags, nil }

func withCompletionNames(ctx context.Context, tools, skills []string) context.Context {
	allowed := make(map[string]struct{}, len(tools)+len(skills))
	for _, name := range append(tools, skills...) {
		allowed[name] = struct{}{}
	}
	return contextinfo.WithToolAvailability(ctx, contextinfo.ToolAvailability{Tools: tools, Skills: skills, Allowed: allowed})
}

func TestToolCompletionPreservesPreparedContextAndReplacementRange(t *testing.T) {
	ctx := contextinfo.WithModel(t.Context(), contextinfo.Model{APIType: "chat"})
	source := ToolDirectiveSource{
		Context: func(ctx context.Context) (context.Context, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			ctx = contextinfo.WithActor(ctx, contextinfo.Actor{Role: contextinfo.RoleUser, ActorIdentity: contextinfo.ActorIdentity{Platform: "cli", ID: "user"}})
			return withCompletionNames(ctx, []string{"visible"}, nil), nil
		},
		Tools: completionCatalog{query: func(ctx context.Context, q tool.ToolQuery) ([]tool.ToolInfo, error) {
			actor, _ := contextinfo.ActorFromContext(ctx)
			model, _ := contextinfo.ModelFromContext(ctx)
			if actor.ID != "user" || model.APIType != "chat" || q.Kind != "" {
				t.Fatalf("context or query lost: %+v %+v %+v", actor, model, q)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return []tool.ToolInfo{{Definition: tool.Info{Name: "visible"}}}, nil
		}},
	}
	cursor := len("中文 @tool:")
	items, err := source.Complete(ctx, Request{Text: "中文 @tool: trailing", Cursor: cursor})
	if err != nil || len(items) != 1 || items[0].Text != "@tool:visible" || items[0].ReplaceStart != len("中文 ") || items[0].ReplaceEnd != cursor {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := source.Complete(canceled, Request{Text: "@tool:v"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestToolCompletionPropagatesPreparationAndQueryErrors(t *testing.T) {
	failure := errors.New("query failed")
	for _, stage := range []string{"context", "tools"} {
		t.Run(stage, func(t *testing.T) {
			catalog := completionCatalog{tags: []string{"web"}}
			source := ToolDirectiveSource{Context: func(ctx context.Context) (context.Context, error) { return ctx, nil }}
			switch stage {
			case "context":
				source.Context = func(context.Context) (context.Context, error) { return nil, failure }
			case "tools":
				catalog.query = func(context.Context, tool.ToolQuery) ([]tool.ToolInfo, error) { return nil, failure }
			}
			source.Tools = catalog
			if _, err := source.Complete(t.Context(), Request{Text: "@tool:w"}); !errors.Is(err, failure) {
				t.Fatal(err)
			}
		})
	}
}
