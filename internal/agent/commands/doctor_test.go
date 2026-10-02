package commands

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"elbot/internal/command"
	"elbot/internal/config"
	"elbot/internal/doctor"
	"elbot/internal/security"
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
	cmd := NewDoctor(Deps{Doctor: service})
	result, err := cmd.Handle(context.Background(), command.Request{})
	if err != nil || result.Content != "Everything is OK" || service.calls != 1 {
		t.Fatalf("result=%#v, err=%v, calls=%d", result, err, service.calls)
	}
	service.report = doctor.Report{ConfigPath: "/config/app.toml", Files: []doctor.FileIssue{{Path: "/config/app.toml", Issues: []config.Issue{{Level: config.LevelError, Message: "文件缺失。"}}}}}
	result, err = cmd.Handle(context.Background(), command.Request{})
	if err != nil || result.Content != service.report.Text() {
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
	if _, err := NewDoctor(Deps{}).Handle(context.Background(), command.Request{}); err == nil {
		t.Fatal("unconfigured doctor reported success")
	}
}

func TestDoctorRegistrationPermissionsHelpAndCompletion(t *testing.T) {
	router := command.NewRouter([]string{"/"})
	deps := Deps{Router: router, Doctor: &doctorStub{}}
	if err := RegisterDefaultModules(router, deps); err != nil {
		t.Fatal(err)
	}
	admin := security.Actor{Role: security.RoleSuperadmin}
	user := security.Actor{Role: security.RoleUser}
	info, ok := router.CommandInfo("doctor")
	if !ok || command.CanAccess(info, user) || !command.CanAccess(info, admin) || info.SessionEffect != command.SessionEffectNone {
		t.Fatalf("doctor info = %#v", info)
	}
	if !slices.Contains(router.CompleteForActor("/doc", admin), "/doctor") {
		t.Fatal("missing admin completion")
	}
	if got := router.CompleteForActor("/doc", user); len(got) != 0 {
		t.Fatalf("user completion = %#v", got)
	}
	for _, actor := range []security.Actor{admin, user} {
		ctx := security.WithActor(context.Background(), actor)
		result, err := NewHelp(deps).Handle(ctx, command.Request{Args: "doctor", Prefix: "/"})
		if err != nil {
			t.Fatal(err)
		}
		if actor.Role == security.RoleSuperadmin {
			if !strings.Contains(result.Content, "只读检查") {
				t.Fatal(result.Content)
			}
		} else if result.Content != "unknown command: doctor" {
			t.Fatal(result.Content)
		}
	}
}
