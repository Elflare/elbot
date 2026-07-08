package completion

import (
	"context"
	"testing"

	"elbot/internal/command"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/tool"
)

type staticSource []Item

func (s staticSource) Complete(context.Context, Request) []Item { return s }

func TestServiceReturnsFirstNonEmptySource(t *testing.T) {
	svc := NewService(staticSource(nil), staticSource{{Text: "/model"}}, staticSource{{Text: "/models"}})
	got := svc.Complete(context.Background(), Request{Text: "/m"})
	if len(got) != 1 || got[0].Text != "/model" {
		t.Fatalf("Complete = %#v", got)
	}
}

func TestRouterSourceCompletesCommands(t *testing.T) {
	router := command.NewRouter([]string{"/"})
	if err := router.Register(command.NewFunc(command.Info{Name: "model", Aliases: []string{"m"}}, nil)); err != nil {
		t.Fatalf("register model: %v", err)
	}
	if err := router.Register(command.NewFunc(command.Info{Name: "models"}, nil)); err != nil {
		t.Fatalf("register models: %v", err)
	}
	items := RouterSource{Router: router}.Complete(context.Background(), Request{Text: "/m"})
	if len(items) != 3 || items[0].Text != "/model" || items[1].Text != "/m" || items[2].Text != "/models" {
		t.Fatalf("Complete = %#v", items)
	}
	for _, item := range items {
		if item.Kind != KindCommand {
			t.Fatalf("item kind = %q", item.Kind)
		}
	}
}

func TestToolDirectiveSourceCompletesOnlyPlainTools(t *testing.T) {
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(completionTestTool{name: "web_search", description: "search", tags: []string{"web"}})
	_ = registry.Register(completionTestTool{name: "web_extract", description: "extract", tags: []string{"web"}})
	_ = registry.Register(completionTestTool{name: "shell", description: "shell"})
	_ = registry.Register(completionTestTool{name: "hidden_tool", hidden: true})
	_ = registry.Register(completionTestSkill{completionTestTool: completionTestTool{name: "docx"}})
	source := ToolDirectiveSource{
		Registry: func() *tool.Registry { return registry },
		Actor:    func(context.Context) security.Actor { return security.Actor{Role: security.RoleSuperadmin} },
	}

	items := source.Complete(context.Background(), Request{Text: "查 @tool:we", Cursor: len("查 @tool:we")})
	if len(items) != 3 || items[0].Text != "@tool:web" || items[0].Label != "web <tag>" || items[0].Kind != KindToolTagDirective || items[1].Text != "@tool:web_extract" || items[2].Text != "@tool:web_search" || items[0].ReplaceStart != len("查 ") || items[0].ReplaceEnd != len("查 @tool:we") {
		t.Fatalf("Complete @tool prefix = %#v", items)
	}
	items = source.Complete(context.Background(), Request{Text: "查 @t", Cursor: len("查 @t")})
	if len(items) != 1 || items[0].Text != "@t:" || items[0].ReplaceStart != len("查 ") || items[0].ReplaceEnd != len("查 @t") {
		t.Fatalf("Complete @t prefix = %#v", items)
	}
	items = source.Complete(context.Background(), Request{Text: "@tool:shl", Cursor: len("@tool:shl")})
	if len(items) != 1 || items[0].Text != "@tool:shell" {
		t.Fatalf("fuzzy complete = %#v", items)
	}
	items = source.Complete(context.Background(), Request{Text: "@t:shl", Cursor: len("@t:shl")})
	if len(items) != 1 || items[0].Text != "@t:shell" {
		t.Fatalf("short fuzzy complete = %#v", items)
	}
	items = source.Complete(context.Background(), Request{Text: "@tool：shl", Cursor: len("@tool：shl")})
	if len(items) != 1 || items[0].Text != "@tool：shell" {
		t.Fatalf("full-width colon complete = %#v", items)
	}
	if got := source.Complete(context.Background(), Request{Text: "@tool:d", Cursor: len("@tool:d")}); len(got) != 0 {
		t.Fatalf("skill should not complete: %#v", got)
	}
}

func TestToolDirectiveSourceCompletesSkillsSeparately(t *testing.T) {
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(completionTestTool{name: "doc_tool", description: "plain"})
	_ = registry.Register(completionTestSkill{completionTestTool: completionTestTool{name: "docx", description: "skill"}})
	source := ToolDirectiveSource{
		Registry: func() *tool.Registry { return registry },
		Actor:    func(context.Context) security.Actor { return security.Actor{Role: security.RoleSuperadmin} },
	}

	items := source.Complete(context.Background(), Request{Text: "查 @skill:do", Cursor: len("查 @skill:do")})
	if len(items) != 1 || items[0].Text != "@skill:docx" || items[0].Kind != KindSkillDirective || items[0].ReplaceStart != len("查 ") || items[0].ReplaceEnd != len("查 @skill:do") {
		t.Fatalf("Complete @skill prefix = %#v", items)
	}
	items = source.Complete(context.Background(), Request{Text: "查 @s", Cursor: len("查 @s")})
	if len(items) != 1 || items[0].Text != "@s:" {
		t.Fatalf("Complete @s prefix = %#v", items)
	}
	items = source.Complete(context.Background(), Request{Text: "查 @s:do", Cursor: len("查 @s:do")})
	if len(items) != 1 || items[0].Text != "@s:docx" || items[0].Kind != KindSkillDirective {
		t.Fatalf("Complete @s prefix = %#v", items)
	}
	items = source.Complete(context.Background(), Request{Text: "查 @skill：do", Cursor: len("查 @skill：do")})
	if len(items) != 1 || items[0].Text != "@skill：docx" || items[0].Kind != KindSkillDirective {
		t.Fatalf("Complete full-width skill prefix = %#v", items)
	}
	if got := source.Complete(context.Background(), Request{Text: "@skill:doc_", Cursor: len("@skill:doc_")}); len(got) != 0 {
		t.Fatalf("plain tool should not complete as skill: %#v", got)
	}
}

func TestToolDirectiveSourceCompletesConfiguredTags(t *testing.T) {
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(completionTestTool{name: "alpha", description: "alpha"})
	source := ToolDirectiveSource{
		Registry: func() *tool.Registry { return registry },
		Actor:    func(context.Context) security.Actor { return security.Actor{Role: security.RoleSuperadmin} },
		Tags: func(context.Context, *tool.Registry, security.Actor, *security.Policy) []string {
			return []string{"agent"}
		},
		ToolNamesByTag: func(_ context.Context, _ *tool.Registry, tag string, _ func(tool.Tool) bool) []string {
			if tag == "agent" {
				return []string{"alpha"}
			}
			return nil
		},
	}

	items := source.Complete(context.Background(), Request{Text: "@tool:ag", Cursor: len("@tool:ag")})
	if len(items) != 1 || items[0].Text != "@tool:agent" || items[0].Description != "1 tool" {
		t.Fatalf("Complete configured tag = %#v", items)
	}
}

func TestRouterSourceCompletesCommandArgs(t *testing.T) {
	router := command.NewRouter([]string{"/"})
	if err := router.Register(commandArgCompleter{}); err != nil {
		t.Fatal(err)
	}
	items := RouterSource{Router: router}.Complete(context.Background(), Request{Text: "/help mo", Cursor: len("/help mo")})
	if len(items) != 1 || items[0].Text != "model" || items[0].ReplaceStart != len("/help ") || items[0].ReplaceEnd != len("/help mo") {
		t.Fatalf("Complete = %#v", items)
	}
}

type completionTestTool struct {
	name        string
	description string
	hidden      bool
	tags        []string
}

func (t completionTestTool) Name() string { return t.name }
func (t completionTestTool) Info() tool.Info {
	return tool.Info{Name: t.name, Description: t.description, Risk: tool.RiskLow, Hidden: t.hidden, Tags: t.tags}
}
func (t completionTestTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{Name: t.name}}
}
func (t completionTestTool) Call(context.Context, tool.CallRequest) (*tool.Result, error) {
	return nil, nil
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
