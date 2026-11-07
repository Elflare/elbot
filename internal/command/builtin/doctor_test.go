package builtin

import (
	"context"
	"errors"
	"testing"

	command "elbot/internal/command/share"
	"elbot/internal/config"
	"elbot/internal/doctor"
)

type doctorStub struct {
	report doctor.Report
	err    error
	calls  int
}

func (d *doctorStub) Check(context.Context) (doctor.Report, error) {
	d.calls++
	return d.report, d.err
}

func TestDoctorCommandUsesDiagnosisAndRejectsArguments(t *testing.T) {
	service := &doctorStub{}
	cmd := NewDoctor(Deps{CommandText: testCommandText, Doctor: service})
	result, err := cmd.Handle(context.Background(), command.Request{})
	if err != nil || commandResultText(result) != "Everything is OK" || service.calls != 1 {
		t.Fatalf("result=%#v, err=%v, calls=%d", result, err, service.calls)
	}
	service.report = doctor.Report{ConfigPath: "/config/app.toml", Files: []doctor.FileIssue{{Path: "/config/app.toml", Issues: []config.Issue{{Level: config.LevelError, Message: "文件缺失。"}}}}}
	result, err = cmd.Handle(context.Background(), command.Request{})
	if err != nil || commandResultText(result) != service.report.Text() {
		t.Fatalf("result=%#v, err=%v", result, err)
	}
	for _, arg := range []string{"fix", "diff", "--config custom.toml"} {
		if _, err := cmd.Handle(context.Background(), command.Request{Args: arg}); err == nil {
			t.Fatalf("accepted arguments %q", arg)
		}
	}
	if service.calls != 2 {
		t.Fatal("ran diagnosis for invalid arguments")
	}
	service.err = errors.New("inspection failed")
	if _, err := cmd.Handle(context.Background(), command.Request{}); !errors.Is(err, service.err) {
		t.Fatalf("error = %v", err)
	}
	if _, err := NewDoctor(Deps{CommandText: testCommandText}).Handle(context.Background(), command.Request{}); err == nil {
		t.Fatal("unconfigured doctor reported success")
	}
}
