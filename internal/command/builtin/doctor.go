package builtin

import (
	"context"
	"fmt"
	"strings"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
)

type doctorCommand struct{ deps Deps }

func NewDoctor(deps Deps) command.Handler { return doctorCommand{deps: deps} }

func (c doctorCommand) Info() command.Info {
	return command.Info{
		Name:        "doctor",
		Args:        "",
		Description: "检查配置与内置 Skill",
		Help:        "只读；发现问题→给出可复制的修复请求",
		MinRole:     contextinfo.RoleSuperadmin,
	}
}

func (c doctorCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	if strings.TrimSpace(req.Args) != "" {
		return nil, inputError(fmt.Errorf("usage: %s", c.deps.CommandText("doctor")))
	}
	if c.deps.Doctor == nil {
		return nil, fmt.Errorf("doctor service is not configured")
	}
	report, err := c.deps.Doctor.Check(ctx)
	if err != nil {
		return nil, err
	}
	items := []command.DoctorIssue{}
	for _, file := range report.Files {
		for _, issue := range file.Issues {
			items = append(items, command.DoctorIssue{Path: file.Path, Level: issue.Level, Topic: issue.Topic, Message: issue.Message, Field: issue.Field, Missing: issue.Missing})
		}
	}
	return &command.Result{Kind: command.ResultKindDoctor, Data: command.DoctorData{ConfigPath: report.ConfigPath, Items: items}}, nil
}

type DoctorModule struct{}

func (DoctorModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewDoctor)
}
