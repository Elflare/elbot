package builtin

import (
	"context"
	"reflect"
	"testing"
	"time"

	command "elbot/internal/command/share"
	"elbot/internal/control"
	"elbot/internal/fileops"
)

type rollbackControlStub struct {
	command.ControlCapabilities
	items   []fileops.RollbackInfo
	result  fileops.RollbackResult
	request control.RollbackByIDRequest
}

func (s *rollbackControlStub) ListRollbacks(context.Context) ([]fileops.RollbackInfo, error) {
	return s.items, nil
}

func (s *rollbackControlStub) RollbackByID(_ context.Context, req control.RollbackByIDRequest) (fileops.RollbackResult, error) {
	s.request = req
	return s.result, nil
}

func TestRollbackCommandReturnsControlDataUnchanged(t *testing.T) {
	items := []fileops.RollbackInfo{{ID: 7, Path: "workspace/note.txt", Target: "s", Created: true, EditedAt: time.Unix(123, 0), RevisionBefore: "before", RevisionAfter: "after"}}
	want := fileops.RollbackResult{Path: "workspace/note.txt", Deleted: true, Revision: "before"}
	service := &rollbackControlStub{items: items, result: want}
	handler := NewRollback(Deps{Control: service, CommandText: testCommandText})
	listed, err := handler.Handle(t.Context(), command.Request{})
	if err != nil || listed == nil || !reflect.DeepEqual(listed.Data, items) {
		t.Fatalf("list result=%#v err=%v", listed, err)
	}
	reverted, err := handler.Handle(t.Context(), command.Request{Args: "7"})
	if err != nil || reverted == nil || !reflect.DeepEqual(reverted.Data, want) || service.request.ID != 7 {
		t.Fatalf("rollback result=%#v request=%+v err=%v", reverted, service.request, err)
	}
}
