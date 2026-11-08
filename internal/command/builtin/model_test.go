package builtin

import (
	"context"
	"reflect"
	"testing"

	command "elbot/internal/command/share"
	"elbot/internal/control"
)

type modelControlStub struct {
	command.ControlCapabilities
	listed     control.ListModelsResult
	refreshed  control.RefreshModelsResult
	selected   control.SelectModelResult
	listReq    control.ListModelsRequest
	selectReq  control.SelectModelRequest
	refreshReq control.ListModelsRequest
}

func (s *modelControlStub) ListModels(_ context.Context, req control.ListModelsRequest) (control.ListModelsResult, error) {
	s.listReq = req
	return s.listed, nil
}
func (s *modelControlStub) RefreshModels(_ context.Context, req control.ListModelsRequest) (control.RefreshModelsResult, error) {
	s.refreshReq = req
	return s.refreshed, nil
}
func (s *modelControlStub) SelectModel(_ context.Context, req control.SelectModelRequest) (control.SelectModelResult, error) {
	s.selectReq = req
	return s.selected, nil
}

func TestModelCommandPassesSelectionToControlAndReturnsRawData(t *testing.T) {
	controlStub := &modelControlStub{
		listed:   control.ListModelsResult{Items: []control.ModelData{{Provider: "p", Model: "m", Index: 2}}},
		selected: control.SelectModelResult{Target: "work", Model: control.ModelData{Provider: "p", Model: "m"}},
	}
	result, err := NewModel(Deps{Control: controlStub, CommandText: testCommandText}).Handle(t.Context(), command.Request{Args: "--work p/m"})
	if err != nil || result == nil || !reflect.DeepEqual(result.Data, controlStub.selected) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if controlStub.listReq.Query != "p/m" || controlStub.selectReq != (control.SelectModelRequest{Target: "work", Provider: "p", Model: "m"}) {
		t.Fatalf("list=%+v select=%+v", controlStub.listReq, controlStub.selectReq)
	}
}

func TestModelAndCheckModelCompletionUseControlCatalog(t *testing.T) {
	controlStub := &modelControlStub{listed: control.ListModelsResult{Items: []control.ModelData{{Provider: "openai", Model: "gpt-4.1"}}}}
	completer := NewModel(Deps{Control: controlStub, CommandText: testCommandText}).(command.Completer)
	got := completer.Complete(t.Context(), command.CompletionRequest{Raw: "/model gpt", Prefix: "/", Name: "model", Args: "gpt", Cursor: len("/model gpt")})
	if len(got) != 1 || got[0].Text != "openai/gpt-4.1" || controlStub.listReq.Query != "gpt" {
		t.Fatalf("completion=%#v request=%+v", got, controlStub.listReq)
	}
	check := NewCheckModel(Deps{Control: controlStub})
	result, err := check.Handle(t.Context(), command.Request{Args: "--fresh gpt"})
	if err != nil || result == nil || result.Kind != command.ResultKindModels || controlStub.refreshReq.Query != "gpt" {
		t.Fatalf("result=%#v refresh=%+v err=%v", result, controlStub.refreshReq, err)
	}
}

func TestModelArgumentParsing(t *testing.T) {
	for _, tc := range []struct {
		args   string
		query  string
		target modelTarget
		bad    bool
	}{
		{args: "--work p/m", query: "p/m", target: modelTargetWork},
		{args: "-c gpt", query: "gpt", target: modelTargetCompact},
		{args: "--work --chat gpt", bad: true},
	} {
		query, target, err := parseModelArgs(tc.args)
		if (err != nil) != tc.bad || query != tc.query || target != tc.target {
			t.Fatalf("parseModelArgs(%q) = (%q, %q, %v)", tc.args, query, target, err)
		}
	}
	query, fresh := parseModelListArgs("--refresh gpt-4.1")
	if query != "gpt-4.1" || !fresh {
		t.Fatalf("parseModelListArgs = (%q, %v)", query, fresh)
	}
}
