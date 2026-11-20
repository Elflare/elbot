package builtin

import (
	"testing"

	command "elbot/internal/command/share"
	"elbot/internal/control"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
)

func TestStopReply(t *testing.T) {
	for _, n := range []int{0, 2} {
		data := control.StopResult{State: "stopping", AcceptedCount: n}
		r, err := stopReply(operationresult.Result[control.StopResult]{Status: operationresult.Success, Data: data}, nil)
		got, ok := r.Data.(control.StopResult)
		if err != nil || r.Kind != command.ResultKindStop || !ok || got.AcceptedCount != n || delivery.ContentBlocksText(r.Blocks) != "stopped" {
			t.Fatalf("accepted_count=%d: %+v, %v", n, r, err)
		}
	}
	failure := operationresult.Result[control.StopResult]{Status: operationresult.NotFound, Message: "request not found"}
	r, err := stopReply(failure, nil)
	if err != nil || r.Status != failure.Status || delivery.ContentBlocksText(r.Blocks) != failure.Message {
		t.Fatalf("failed stop: %+v, %v", r, err)
	}
}
