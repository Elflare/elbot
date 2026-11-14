package control

import (
	"context"
	"elbot/internal/operationresult"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/control/share"
)

// Supervisor 持有统一操作策略和文件访问范围，不拥有借用业务服务的生命周期。
type Supervisor struct {
	deps        SupervisorDeps
	workspaces  WorkspaceCapabilities
	roots       map[string]string // 逻辑目录到实际目录；构造后只读。
	configFiles map[string]string // 固定逻辑配置名到实际文件，可引用配置目录之外的文件。
	filesMu     sync.RWMutex      // 协调本总管的直接读写，防止读到本总管尚未写完的内容。
}

// NewSupervisor 接收 Config 已加载并应用默认值的配置；C 不重复定义配置默认值。
func NewSupervisor(cfg *config.Config, deps SupervisorDeps) (*Supervisor, error) {
	if cfg == nil || strings.TrimSpace(cfg.ConfigPath) == "" {
		return nil, fmt.Errorf("control: ConfigPath is required")
	}
	if deps.Sessions != nil && deps.Scope == nil {
		return nil, fmt.Errorf("control: session Scope resolver is required")
	}
	configPath, err := filepath.Abs(cfg.ConfigPath)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(configPath)
	m := &Supervisor{deps: deps,
		roots:       map[string]string{"skills": filepath.Join(dir, "skills"), "plugins": filepath.Join(dir, "plugins"), "memory": dir, "frontend": filepath.Join(dir, "frontend")},
		configFiles: map[string]string{"app.toml": configPath, ".env": filepath.Join(dir, ".env"), ".env.example": filepath.Join(dir, ".env.example")}}
	for name, path := range map[string]string{"providers.toml": cfg.ProvidersConfigPath, "state.toml": cfg.StateConfigPath, "elnis.toml": cfg.ElnisConfigPath, "tool_tags.toml": cfg.ToolTagsConfigPath, "SOUL.md": cfg.Soul.Path} {
		if path != "" {
			m.configFiles[name] = path
		}
	}
	if deps.LogDir != "" {
		path, err := filepath.Abs(deps.LogDir)
		if err != nil {
			return nil, err
		}
		m.roots["logs"] = path
	}
	return m, nil
}

func (m *Supervisor) ListCommands(ctx context.Context, req ListCommandsRequest) (operationresult.Result[ListCommandsResult], error) {
	authorized, err := share.Authorize(ctx, false)
	if err != nil {
		return operationresult.Result[ListCommandsResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[ListCommandsResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	actor := authorized.Data
	result := ListCommandsResult{Items: []CommandInfo{}}
	items, err := m.deps.Commands(ctx)
	if err != nil {
		return operationresult.Result[ListCommandsResult]{}, err
	}
	for _, item := range items {
		if item.MinRole != contextinfo.RoleUser && actor.Role != contextinfo.RoleSuperadmin {
			continue
		}
		if req.Name != "" && item.Name != req.Name && !slices.Contains(item.Aliases, req.Name) {
			continue
		}
		item.Aliases = slices.Clone(item.Aliases)
		result.Items = append(result.Items, item)
	}
	if req.Name != "" && len(result.Items) == 0 {
		return operationresult.Result[ListCommandsResult]{Status: operationresult.NotFound, Message: "命令不存在或不可用"}, nil
	}
	return operationresult.Result[ListCommandsResult]{Status: operationresult.Success, Data: result}, nil
}

func (m *Supervisor) Status(ctx context.Context) (operationresult.Result[StatusResult], error) {
	authorized, err := share.Authorize(ctx, false)
	if err != nil {
		return operationresult.Result[StatusResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[StatusResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	return m.deps.Status(ctx)
}
