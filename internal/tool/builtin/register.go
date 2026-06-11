package builtin

import (
	elcron "elbot/internal/cron"
	"elbot/internal/memory/resident"
	"elbot/internal/tool"
	"elbot/internal/tool/skill"
)

type RegisterOptions struct {
	ResidentMemoryStore *resident.Store
	SkillManager        *skill.Manager
	CronService         *elcron.Service
	LongMemoryDir       string
}

func RegisterAll(registry *tool.Registry, opts RegisterOptions) error {
	if err := registry.Register(tool.NewDiscoverTool(registry)); err != nil {
		return err
	}
	if opts.ResidentMemoryStore != nil {
		if err := registry.Register(NewResidentMemoryTool(opts.ResidentMemoryStore)); err != nil {
			return err
		}
	}
	if longMemoryDir := opts.LongMemoryDir; longMemoryDir != "" {
		for _, memoryTool := range NewLongMemoryTools(longMemoryDir) {
			if err := registry.Register(memoryTool); err != nil {
				return err
			}
		}
	}
	if opts.CronService != nil {

		for _, cronTool := range NewCronTools(opts.CronService) {
			if err := registry.Register(cronTool); err != nil {
				return err
			}
		}
	}
	if err := registry.Register(NewWebSearchTool()); err != nil {
		return err
	}
	if err := registry.Register(NewWebExtractTool()); err != nil {
		return err
	}
	if err := registry.Register(NewShellTool()); err != nil {
		return err
	}
	catalog := (*skill.Catalog)(nil)
	if opts.SkillManager != nil {
		catalog = opts.SkillManager.Catalog
	}
	if err := registry.Register(skill.NewPythonRunner(catalog)); err != nil {
		return err
	}
	if err := registry.Register(skill.NewGoRunner(catalog)); err != nil {
		return err
	}
	if opts.SkillManager != nil {
		if err := registry.Register(skill.NewCreateElSkillTool(opts.SkillManager)); err != nil {
			return err
		}
		if err := registry.Register(skill.NewReadElSkillTool(opts.SkillManager)); err != nil {
			return err
		}
		if err := registry.Register(skill.NewModifyElSkillTool(opts.SkillManager)); err != nil {
			return err
		}
	}

	return nil
}
