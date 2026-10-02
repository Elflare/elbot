package commands

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/command"
	"elbot/internal/security"
)

type doctorCommand struct{ deps Deps }

func NewDoctor(deps Deps) command.Handler { return doctorCommand{deps: deps} }

func (c doctorCommand) Info() command.Info {
	return command.Info{
		Name:        "doctor",
		Usage:       "/doctor",
		Description: "检查配置与内置 Skill，生成可复制给 Elbot 的处理请求。",
		Help:        "只读检查当前服务的配置文件和内置 Skill；按文件列出缺失项、配置错误及 Skill 差异，并附上配置说明和默认模板地址。未知字段仅检查主配置；没有问题时返回 Everything is OK。不会自动修改文件。",
		MinRole:     security.RoleSuperadmin,
	}
}

func (c doctorCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	if strings.TrimSpace(req.Args) != "" {
		return nil, fmt.Errorf("usage: /doctor")
	}
	if c.deps.Doctor == nil {
		return nil, fmt.Errorf("doctor service is not configured")
	}
	report, err := c.deps.Doctor.Check(ctx)
	if err != nil {
		return nil, err
	}
	return &command.Result{Content: report.Text()}, nil
}

type DoctorModule struct{}

func (DoctorModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps, NewDoctor)
}
