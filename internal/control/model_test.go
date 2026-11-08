package control

import (
	"context"
	"testing"

	"elbot/internal/modelmgr"
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

func (s *modelStub) PrepareModel(arg string) (modelmgr.ModelOption, error) {
	s.prepared = arg
	return s.selected, s.prepareErr
}
func (s *modelStub) CommitModelForMode(mode string, selected modelmgr.ModelOption) (modelmgr.ModelOption, error) {
	s.mode = mode
	s.commits++
	return selected, nil
}
func (s *modelStub) ReloadProviders(context.Context) error { s.reloads++; return nil }
func (s *modelStub) ModelList(query string, opts modelmgr.ModelListOptions) modelmgr.ModelListResult {
	s.fresh = opts.Fresh
	s.query = query
	return s.list
}

func TestModelSelectionUsesExplicitIdentityAndDefaultMode(t *testing.T) {
	provider := &modelStub{selected: modelmgr.ModelOption{Provider: "p", Model: "m"}}
	sessions := &sessionStub{currentErr: session.ErrNoCurrentSession}
	m, _ := testSupervisor(t, SupervisorDeps{Models: provider, Sessions: sessions, Scope: func(context.Context) session.Scope { return session.Scope{} }})
	_, err := m.SelectModel(testContext(false), SelectModelRequest{Provider: "p", Model: "m"})
	requireCode(t, err, "permission_denied")
	result, err := m.SelectModel(testContext(true), SelectModelRequest{Provider: "p", Model: "m", Target: "current"})
	if err != nil {
		t.Fatal(err)
	}
	if provider.prepared != "p/m" || provider.mode != "work" || result.Target != "current" {
		t.Fatalf("selection: %+v %+v", result, provider)
	}
	_, err = m.SelectModel(testContext(true), SelectModelRequest{Provider: "p", Model: "m", Target: "elwisp2"})
	if err != nil || provider.mode != "elwisp2" {
		t.Fatalf("slot selection: %s %v", provider.mode, err)
	}
	_, err = m.SelectModel(testContext(true), SelectModelRequest{Provider: "p", Model: "m", Target: "unknown"})
	requireCode(t, err, "invalid_arguments")
	_, err = m.SelectModel(testContext(true), SelectModelRequest{Provider: "p", Model: "m"})
	requireCode(t, err, "invalid_arguments")
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
	if provider.reloads != 1 || !provider.fresh || provider.query != "chosen model" || result.Query != "chosen model" || len(result.Items) != 1 || len(result.Errors) != 1 {
		t.Fatalf("refresh: %+v", result)
	}
	requireCode(t, result.Errors[0].Error, "timeout")
	result.Items[0].Modes[0] = "changed"
	if provider.list.Options[0].ModeMarks[0] != "work" {
		t.Fatal("model result mutated source")
	}
}

func TestElwispSelectionDoesNotNeedSessions(t *testing.T) {
	provider := &modelStub{selected: modelmgr.ModelOption{Provider: "p", Model: "m"}}
	m, _ := testSupervisor(t, SupervisorDeps{Models: provider})
	for _, target := range []string{"elwisp1", "elwisp2", "elwisp3"} {
		result, err := m.SelectModel(testContext(true), SelectModelRequest{Provider: "p", Model: "m", Target: target})
		if err != nil || provider.mode != target || provider.prepared != "p/m" || result.Target != target {
			t.Fatalf("selection for %s: %+v %v", target, result, err)
		}
	}
	provider.prepareErr = context.DeadlineExceeded
	_, err := m.SelectModel(testContext(true), SelectModelRequest{Provider: "p", Model: "m", Target: "elwisp1"})
	requireCode(t, err, "timeout")
	if provider.commits != 3 {
		t.Fatal("failed preparation reached model commit")
	}
}
