package builtin

import (
	"context"
	"elbot/internal/operationresult"
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

func (c doctorCommand) Handle(ctx context.Context, req command.Request) (command.Result, error) {
	if strings.TrimSpace(req.Args) != "" {
		return command.Result{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("usage: %s", c.deps.CommandText("doctor"))}, nil
	}
	data, err := c.deps.Control.CheckConfiguration(ctx)
	if err != nil {
		return command.Result{}, err
	}
	return command.FromOperationResult(data, command.ResultKindDoctor), nil
}

type DoctorModule struct{}

func (DoctorModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewDoctor)
}
