package completion

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"errors"
	"strings"
	"testing"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/tool"
	"elbot/internal/tool/discovery"
	"elbot/internal/tool/preload"
	"elbot/internal/tool/registry"
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
	items, _ := RouterSource{Router: router}.Complete(context.Background(), Request{Text: "/m"})
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

	userSource := RouterSource{Router: router, Actor: func(context.Context) contextinfo.Actor { return contextinfo.Actor{Role: contextinfo.RoleUser} }}
	items, _ := userSource.Complete(context.Background(), Request{Text: "/"})
	if len(items) != 2 || items[0].Text != "/public" || items[1].Text != "/p" {
		t.Fatalf("user command completions = %#v", items)
	}
	if got, _ := userSource.Complete(context.Background(), Request{Text: "/s"}); len(got) != 0 {
		t.Fatalf("user private command completions = %#v", got)
	}
	if got, _ := userSource.Complete(context.Background(), Request{Text: "/help mo", Cursor: len("/help mo")}); len(got) != 0 {
		t.Fatalf("user private argument completions = %#v", got)
	}

	adminSource := RouterSource{Router: router, Actor: func(context.Context) contextinfo.Actor { return contextinfo.Actor{Role: contextinfo.RoleSuperadmin} }}
	if got, _ := adminSource.Complete(context.Background(), Request{Text: "/s"}); len(got) != 2 || got[0].Text != "/secret" || got[1].Text != "/s" {
		t.Fatalf("admin private command completions = %#v", got)
	}
	if got, _ := adminSource.Complete(context.Background(), Request{Text: "/help mo", Cursor: len("/help mo")}); len(got) != 1 || got[0].Text != "model" {
		t.Fatalf("admin private argument completions = %#v", got)
	}
}
func TestToolDirectiveSourceCompletesOnlyPlainTools(t *testing.T) {
	registry := registry.NewRegistry(security.DefaultPermission())
	_ = registry.Register(discovery.NewDiscoverTool(discovery.NewResolver(registry)))
	_ = registry.Register(completionTestTool{name: "web_search", description: "search", tags: []string{"web"}})
	_ = registry.Register(completionTestTool{name: "web_extract", description: "extract", tags: []string{"web"}})
	_ = registry.Register(completionTestTool{name: "shell", description: "shell"})
	_ = registry.Register(completionTestTool{name: "hidden_tool", hidden: true})
	_ = registry.Register(completionTestSkill{completionTestTool: completionTestTool{name: "docx"}})
	source := ToolDirectiveSource{
		Registry: func() ToolCapabilities { return completionCatalog{registry} },
		Actor:    func(context.Context) contextinfo.Actor { return contextinfo.Actor{Role: contextinfo.RoleSuperadmin} },
		Tags: func(ctx context.Context, _ ToolCapabilities, actor contextinfo.Actor, permission *security.Permission) ([]string, error) {
			return preload.NewPreloadService(preload.PreloadOptions{Registry: registry}).Tags(contextinfo.WithActor(security.WithPermission(ctx, permission), actor))
		},
		ToolNamesByTag: func(ctx context.Context, _ ToolCapabilities, tag string, query tool.ToolQuery) ([]string, error) {
			return preload.NewPreloadService(preload.PreloadOptions{Registry: registry}).ToolNamesByTag(ctx, tag, toolshare.ToolQuery{Kind: query.Kind, Names: query.Names, IncludeHidden: query.IncludeHidden, IncludeDiscover: query.IncludeDiscover})
		},
	}

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
	registry := registry.NewRegistry(security.DefaultPermission())
	_ = registry.Register(discovery.NewDiscoverTool(discovery.NewResolver(registry)))
	_ = registry.Register(completionTestTool{name: "doc_tool", description: "plain"})
	_ = registry.Register(completionTestSkill{completionTestTool: completionTestTool{name: "docx", description: "skill"}})
	source := ToolDirectiveSource{
		Registry: func() ToolCapabilities { return completionCatalog{registry} },
		Actor:    func(context.Context) contextinfo.Actor { return contextinfo.Actor{Role: contextinfo.RoleSuperadmin} },
		Tags: func(ctx context.Context, _ ToolCapabilities, actor contextinfo.Actor, permission *security.Permission) ([]string, error) {
			return preload.NewPreloadService(preload.PreloadOptions{Registry: registry}).Tags(contextinfo.WithActor(security.WithPermission(ctx, permission), actor))
		},
		ToolNamesByTag: func(ctx context.Context, _ ToolCapabilities, tag string, query tool.ToolQuery) ([]string, error) {
			return preload.NewPreloadService(preload.PreloadOptions{Registry: registry}).ToolNamesByTag(ctx, tag, toolshare.ToolQuery{Kind: query.Kind, Names: query.Names, IncludeHidden: query.IncludeHidden, IncludeDiscover: query.IncludeDiscover})
		},
	}

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
	registry := registry.NewRegistry(security.DefaultPermission())
	_ = registry.Register(discovery.NewDiscoverTool(discovery.NewResolver(registry)))
	_ = registry.Register(completionTestTool{name: "alpha", description: "alpha"})
	source := ToolDirectiveSource{
		Registry: func() ToolCapabilities { return completionCatalog{registry} },
		Actor:    func(context.Context) contextinfo.Actor { return contextinfo.Actor{Role: contextinfo.RoleSuperadmin} },
		Tags: func(context.Context, ToolCapabilities, contextinfo.Actor, *security.Permission) ([]string, error) {
			return []string{"agent"}, nil
		},
		ToolNamesByTag: func(_ context.Context, _ ToolCapabilities, tag string, _ tool.ToolQuery) ([]string, error) {
			if tag == "agent" {
				return []string{"alpha"}, nil
			}
			return nil, nil
		},
	}

	items, _ := source.Complete(context.Background(), Request{Text: "@tool:ag", Cursor: len("@tool:ag")})
	if len(items) != 1 || items[0].Text != "@tool:agent" || items[0].Description != "1 tool" {
		t.Fatalf("Complete configured tag = %#v", items)
	}
}

func TestRouterSourceCompletesCommandArgs(t *testing.T) {
	router := command.NewRouter([]string{"/"})
	if err := router.Register(commandArgCompleter{}); err != nil {
		t.Fatal(err)
	}
	items, _ := RouterSource{Router: router, Actor: func(context.Context) contextinfo.Actor { return contextinfo.Actor{Role: contextinfo.RoleSuperadmin} }}.Complete(context.Background(), Request{Text: "/help mo", Cursor: len("/help mo")})
	if len(items) != 1 || items[0].Text != "model" || items[0].ReplaceStart != len("/help ") || items[0].ReplaceEnd != len("/help mo") {
		t.Fatalf("Complete = %#v", items)
	}
}

type completionTestTool struct {
	name        string
	description string
	hidden      bool
	adminOnly   bool
	apiTypes    []llm.APIType
	tags        []string
}

func (t completionTestTool) Name() string { return t.name }
func (t completionTestTool) Info() toolshare.Info {
	return toolshare.Info{Name: t.name, Description: t.description, Risk: toolshare.RiskLow, Hidden: t.hidden, Tags: t.tags, SuperadminOnly: t.adminOnly, APITypes: t.apiTypes}
}
func (t completionTestTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: t.name}
}
func (t completionTestTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		return nil, "", nil
	}), nil
}

type completionTestSkill struct{ completionTestTool }

func (s completionTestSkill) Detail() string          { return "detail" }
func (s completionTestSkill) ActivateTools() []string { return nil }

type commandArgCompleter struct{}

func (commandArgCompleter) Info() command.Info { return command.Info{Name: "help"} }
func (commandArgCompleter) Handle(context.Context, command.Request) (*command.Result, error) {
	return nil, nil
}
func (commandArgCompleter) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	_ = ctx
	return []command.Completion{{Text: "model", Label: "model", Kind: "command_arg", ReplaceStart: len("/help "), ReplaceEnd: req.Cursor}}
}

func TestTextsDropsEmptyItems(t *testing.T) {

	got := Texts([]Item{{Text: "/a"}, {}, {Text: "/b"}})
	if len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Fatalf("Texts = %#v", got)
	}
}

type completionCatalog struct{ *registry.Registry }

func (r completionCatalog) QueryTools(ctx context.Context, query tool.ToolQuery) ([]tool.Info, error) {
	values, err := r.Registry.QueryTools(ctx, toolshare.ToolQuery{Kind: query.Kind, Names: query.Names, IncludeHidden: query.IncludeHidden, IncludeDiscover: query.IncludeDiscover})
	if err != nil {
		return nil, err
	}
	infos := make([]tool.Info, 0, len(values))
	for _, value := range values {
		info := value.Info()
		infos = append(infos, tool.Info{Name: info.Name, Description: info.Description, Hidden: info.Hidden, Tags: info.Tags})
	}
	return infos, nil
}

func TestToolCompletionPreservesActorModelAndReplacementRange(t *testing.T) {
	registry := registry.NewRegistry(security.DefaultPermission())
	for _, candidate := range []completionTestTool{
		{name: "visible"},
		{name: "admin", adminOnly: true},
		{name: "hidden", hidden: true},
		{name: "responses", apiTypes: []llm.APIType{llm.APIType("responses")}},
	} {
		if err := registry.Register(candidate); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		role      contextinfo.Role
		api, want string
	}{
		{contextinfo.RoleUser, "", "@tool:visible"},
		{contextinfo.RoleSuperadmin, "responses", "@tool:admin,@tool:responses,@tool:visible"},
	} {
		source := ToolDirectiveSource{
			Registry: func() ToolCapabilities { return completionCatalog{registry} },
			Actor:    func(context.Context) contextinfo.Actor { return contextinfo.Actor{Role: tc.role} },
			Tags: func(context.Context, ToolCapabilities, contextinfo.Actor, *security.Permission) ([]string, error) {
				return nil, nil
			},
		}
		ctx := contextinfo.WithModel(t.Context(), contextinfo.Model{APIType: tc.api})
		text := "中文 @tool: trailing"
		cursor := len("中文 @tool:")
		items, err := source.Complete(ctx, Request{Text: text, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, item := range items {
			names = append(names, item.Text)
			if item.ReplaceStart != len("中文 ") || item.ReplaceEnd != cursor {
				t.Fatalf("range=%+v", item)
			}
		}
		if got := strings.Join(names, ","); got != tc.want {
			t.Fatalf("got=%q want=%q", got, tc.want)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := source.Complete(canceled, Request{Text: "@tool:v"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("query error=%v", err)
		}
	}
}
