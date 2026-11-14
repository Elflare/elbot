package control

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	"elbot/internal/session"
)

type modelStub struct {
	ModelCapabilities
	selected   modelmgr.ModelOption
	mode       string
	prepared   string
	reloads    int
	fresh      bool
	query      string
	list       modelmgr.ModelListResult
	prepareErr error
	commits    int
}

func (s *modelStub) PrepareModel(arg string) (operationresult.Result[modelmgr.ModelOption], error) {
	s.prepared = arg
	if s.prepareErr != nil {
		return operationresult.Result[modelmgr.ModelOption]{}, s.prepareErr
	}
	return operationresult.Result[modelmgr.ModelOption]{Status: operationresult.Success, Data: s.selected}, nil
}
func (s *modelStub) CommitModelForMode(mode string, selected modelmgr.ModelOption) (operationresult.Result[modelmgr.ModelOption], error) {
	s.mode = mode
	s.commits++
	return operationresult.Result[modelmgr.ModelOption]{Status: operationresult.Success, Data: selected}, nil
}
func (s *modelStub) ReloadProviders(context.Context) error { s.reloads++; return nil }
func (s *modelStub) ModelList(query string, opts modelmgr.ModelListOptions) modelmgr.ModelListResult {
	s.fresh = opts.Fresh
	s.query = query
	return s.list
}

func TestModelSelectionUsesExplicitIdentityAndDefaultMode(t *testing.T) {
	provider := &modelStub{selected: modelmgr.ModelOption{Provider: "p", Model: "m"}}
	sessions := &sessionStub{currentStatus: operationresult.NoCurrentSession}
	m, _ := testSupervisor(t, SupervisorDeps{Models: provider, Sessions: sessions, Scope: func(context.Context) session.Scope { return session.Scope{} }})
	forbidden, err := m.SelectModel(testContext(false), SelectModelRequest{Provider: "p", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, forbidden, operationresult.Forbidden)
	result, err := m.SelectModel(testContext(true), SelectModelRequest{Provider: "p", Model: "m", Target: "current"})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if provider.prepared != "p/m" || provider.mode != "work" || result.Data.Target != "current" {
		t.Fatalf("selection: %+v %+v", result, provider)
	}
	slot, err := m.SelectModel(testContext(true), SelectModelRequest{Provider: "p", Model: "m", Target: "elwisp2"})
	if err != nil || slot.Status != operationresult.Success || provider.mode != "elwisp2" {
		t.Fatalf("slot selection: %+v %v", slot, err)
	}
	invalid, err := m.SelectModel(testContext(true), SelectModelRequest{Provider: "p", Model: "m", Target: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, invalid, operationresult.InvalidArguments)
	invalid, err = m.SelectModel(testContext(true), SelectModelRequest{Provider: "p", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, invalid, operationresult.InvalidArguments)
	if provider.commits != 2 {
		t.Fatal("invalid target reached model commit")
	}
}

func TestModelRefreshKeepsPartialErrorsAndDetachesModes(t *testing.T) {
	provider := &modelStub{list: modelmgr.ModelListResult{Options: []modelmgr.ModelOption{{Provider: "p", Model: "m", ModeMarks: []string{"work"}}}, Errors: []modelmgr.ModelProviderError{{Provider: "bad", Err: context.DeadlineExceeded}}}}
	m, _ := testSupervisor(t, SupervisorDeps{Models: provider})
	result, err := m.RefreshModels(testContext(true), ListModelsRequest{Query: "chosen model"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != operationresult.Success || provider.reloads != 1 || !provider.fresh || provider.query != "chosen model" || result.Data.Query != "chosen model" || len(result.Data.Items) != 1 || len(result.Data.Errors) != 1 {
		t.Fatalf("refresh: %+v", result)
	}
	if result.Data.Errors[0].Error != context.DeadlineExceeded.Error() {
		t.Fatalf("provider error = %q", result.Data.Errors[0].Error)
	}
	result.Data.Items[0].Modes[0] = "changed"
	if provider.list.Options[0].ModeMarks[0] != "work" {
		t.Fatal("model result mutated source")
	}
}

func TestElwispSelectionDoesNotNeedSessions(t *testing.T) {
	provider := &modelStub{selected: modelmgr.ModelOption{Provider: "p", Model: "m"}}
	m, _ := testSupervisor(t, SupervisorDeps{Models: provider})
	for _, target := range []string{"elwisp1", "elwisp2", "elwisp3"} {
		result, err := m.SelectModel(testContext(true), SelectModelRequest{Provider: "p", Model: "m", Target: target})
		if err != nil || result.Status != operationresult.Success || provider.mode != target || provider.prepared != "p/m" || result.Data.Target != target {
			t.Fatalf("selection for %s: %+v %v", target, result, err)
		}
	}
	provider.prepareErr = context.DeadlineExceeded
	_, err := m.SelectModel(testContext(true), SelectModelRequest{Provider: "p", Model: "m", Target: "elwisp1"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("prepare error = %v", err)
	}
	if provider.commits != 3 {
		t.Fatal("failed preparation reached model commit")
	}
}
