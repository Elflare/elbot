package builtin

import (
	"context"
	"reflect"
	"testing"

	command "elbot/internal/command/share"
	"elbot/internal/control"
	"elbot/internal/operationresult"
	"elbot/internal/tool"
	toolshare "elbot/internal/tool/share"
)

type toolControlStub struct {
	command.ControlCapabilities
	infos    []tool.ToolInfo
	query    tool.ToolQuery
	removed  control.RemoveSkillRequest
	reloaded bool
	err      error
}

func (s *toolControlStub) QueryTools(_ context.Context, query tool.ToolQuery) (operationresult.Result[[]tool.ToolInfo], error) {
	s.query = query
	return operationresult.Result[[]tool.ToolInfo]{Status: operationresult.Success, Data: s.infos}, s.err
}
func (s *toolControlStub) ReloadSkills(context.Context) (operationresult.Result[struct{}], error) {
	s.reloaded = true
	return operationresult.Result[struct{}]{Status: operationresult.Success}, s.err
}
func (s *toolControlStub) RemoveSkill(_ context.Context, req control.RemoveSkillRequest) (operationresult.Result[struct{}], error) {
	s.removed = req
	return operationresult.Result[struct{}]{Status: operationresult.Success}, s.err
}

func TestToolsCommandConfirmsBeforeCallingControl(t *testing.T) {
	controlStub := &toolControlStub{}
	handler := NewTools(Deps{CommandText: testCommandText, Control: controlStub})
	result, err := handler.Handle(t.Context(), command.Request{Args: "remove echo"})
	if err != nil || result.Status != operationresult.Rejected || result.Confirmation == nil || len(result.Confirmation.Actions) != 1 || result.Confirmation.Actions[0].Command != "/tools remove echo --confirm" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if controlStub.removed.Name != "" {
		t.Fatal("unconfirmed removal reached control")
	}
	result, err = handler.Handle(t.Context(), command.Request{Args: "remove echo --confirm"})
	if err != nil || result.Status != operationresult.Success || controlStub.removed != (control.RemoveSkillRequest{Name: "echo", Confirmed: true}) {
		t.Fatalf("result=%+v request=%+v err=%v", result, controlStub.removed, err)
	}
}

func TestToolsCommandQueriesRawCatalogAndCompletesThroughControl(t *testing.T) {
	controlStub := &toolControlStub{infos: []tool.ToolInfo{{Definition: toolshare.Info{Name: "echo", Description: "echo tool"}}}}
	handler := NewTools(Deps{CommandText: testCommandText, Control: controlStub})
	result, err := handler.Handle(t.Context(), command.Request{})
	if err != nil || result.Status != operationresult.Success || !reflect.DeepEqual(result.Data, controlStub.infos) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if controlStub.query.Scope != tool.QueryCatalog || !controlStub.query.IncludeHidden || !controlStub.query.IncludeDiscover {
		t.Fatalf("query=%+v", controlStub.query)
	}
	completer := handler.(command.Completer)
	got := completer.Complete(t.Context(), command.CompletionRequest{Raw: "/tools remove e", Prefix: "/", Name: "tools", Args: "remove e", Cursor: len("/tools remove e")})
	if len(got) != 1 || got[0].Text != "echo" {
		t.Fatalf("completion=%#v", got)
	}
}
