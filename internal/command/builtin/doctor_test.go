package builtin

import (
	"context"
	"errors"
	"reflect"
	"testing"

	command "elbot/internal/command/share"
	"elbot/internal/control"
)

type doctorControlStub struct {
	command.ControlCapabilities
	result control.CheckConfigurationResult
	err    error
	calls  int
}

func (s *doctorControlStub) CheckConfiguration(context.Context) (control.CheckConfigurationResult, error) {
	s.calls++
	return s.result, s.err
}

func TestDoctorCommandCallsControlAndReturnsItsData(t *testing.T) {
	want := control.CheckConfigurationResult{ConfigPath: "/config/app.toml", Items: []control.DoctorIssue{{Path: "/config/providers.toml", Level: "error", Message: "missing"}}}
	service := &doctorControlStub{result: want}
	cmd := NewDoctor(Deps{CommandText: testCommandText, Control: service})
	result, err := cmd.Handle(t.Context(), command.Request{})
	if err != nil || service.calls != 1 || result == nil || !reflect.DeepEqual(result.Data, want) {
		t.Fatalf("result=%#v err=%v calls=%d", result, err, service.calls)
	}
	for _, arg := range []string{"fix", "diff", "--config custom.toml"} {
		if _, err := cmd.Handle(t.Context(), command.Request{Args: arg}); err == nil {
			t.Fatalf("accepted arguments %q", arg)
		}
	}
	if service.calls != 1 {
		t.Fatal("invalid arguments invoked control")
	}
	failure := errors.New("inspection failed")
	service.err = failure
	if _, err := cmd.Handle(t.Context(), command.Request{}); !errors.Is(err, failure) {
		t.Fatalf("error=%v", err)
	}
}
