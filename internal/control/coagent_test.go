package control

import (
	"context"
	"math"
	"slices"
	"testing"

	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type coagentExecutionStub struct {
	executionStub
	snapshots []turn.Snapshot
}

func (e coagentExecutionStub) SnapshotAll() []turn.Snapshot { return e.snapshots }

func TestCoagentStatusIsReadOnlyAndGroupScoped(t *testing.T) {
	m, _ := testSupervisor(t, SupervisorDeps{Execution: &coagentExecutionStub{snapshots: []turn.Snapshot{{SessionID: "outside", Phase: turn.PhaseLLM}, {SessionID: "c1", Phase: turn.PhaseTool, PendingCount: 2}}},
		Coagents: func(_ context.Context, a session.CoagentAction, user bool) (operationresult.Result[storage.CoagentBoard], error) {
			if !user || a.Operation != "board" || a.After != math.MaxInt64 {
				t.Fatal(a)
			}
			return operationresult.Result[storage.CoagentBoard]{Status: operationresult.Success, Data: storage.CoagentBoard{MainSessionID: "m", Members: []storage.CoagentMember{{SessionID: "m", Name: "主"}, {SessionID: "c1", Name: "审阅"}}}}, nil
		}})
	want := []CoagentStatus{{SessionID: "m", Name: "主", Phase: turn.PhaseIdle}, {SessionID: "c1", Name: "审阅", Phase: turn.PhaseTool, PendingCount: 2}}
	for _, tc := range []struct {
		target string
		want   []CoagentStatus
	}{{"", want}, {"c1", want[1:]}} {
		got, err := m.CoagentStatus(testContext(false), tc.target)
		if err != nil || got.Status != operationresult.Success || !slices.Equal(got.Data, tc.want) {
			t.Fatalf("%s: %+v, %v", tc.target, got, err)
		}
	}
	got, err := m.CoagentStatus(testContext(false), "outside")
	if err != nil || got.Status != operationresult.NotFound || len(got.Data) != 0 {
		t.Fatal(got, err)
	}
}
