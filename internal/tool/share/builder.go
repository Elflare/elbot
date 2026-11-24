// 构造工具元数据和参数 schema。
package share

import (
	"context"
	"slices"

	"elbot/internal/llm"
	"elbot/internal/operationresult"
)

type Builder struct {
	info       Info // 构建中的目录元数据；BuildInfo 返回独立的切片。
	properties map[string]any
	required   []string
}

type ParamOption func(*paramConfig)

type paramConfig struct {
	required bool
	enum     []string
}

func NewBuilder(name string) *Builder {
	return &Builder{info: Info{Name: name}, properties: map[string]any{}}
}

func (b *Builder) Description(description string) *Builder {
	b.info.Description = description
	return b
}

func (b *Builder) Async(mode AsyncMode) *Builder {
	b.info.Async = mode
	b.Boolean("async", "异步执行，长耗时调用可true。")
	b.properties["async"].(map[string]any)["default"] = mode == AsyncByDefault
	return b
}
func (b *Builder) Weavable() *Builder {
	b.info.Weavable = true
	return b
}

func (b *Builder) NeedsElyphRules() *Builder {
	b.info.NeedsElyphRules = true
	return b
}

func (b *Builder) Risk(risk RiskLevel) *Builder {
	b.info.Risk = risk
	return b
}

func (b *Builder) Hidden() *Builder {
	b.info.Hidden = true
	return b
}

func (b *Builder) Tags(tags ...string) *Builder {
	b.info.Tags = append(b.info.Tags, tags...)
	return b
}

func (b *Builder) SuperadminOnly() *Builder {
	b.info.SuperadminOnly = true
	return b
}

func (b *Builder) OwnerScoped() *Builder {
	b.info.OwnerScoped = true
	return b
}

func (b *Builder) ForegroundOnly() *Builder {
	b.info.ForegroundOnly = true
	return b
}

func (b *Builder) APITypes(types ...llm.APIType) *Builder {
	b.info.APITypes = append([]llm.APIType(nil), types...)
	return b
}

func (b *Builder) DependsOn(names ...string) *Builder {
	b.info.DependsOn = append(b.info.DependsOn, names...)
	return b
}

func (b *Builder) String(name, description string, opts ...ParamOption) *Builder {
	return b.param(name, "string", description, opts...)
}

func (b *Builder) Integer(name, description string, opts ...ParamOption) *Builder {
	return b.param(name, "integer", description, opts...)
}

func (b *Builder) Boolean(name, description string, opts ...ParamOption) *Builder {
	return b.param(name, "boolean", description, opts...)
}

func (b *Builder) Object(name, description string, opts ...ParamOption) *Builder {
	b.param(name, "object", description, opts...)
	b.properties[name].(map[string]any)["additionalProperties"] = true
	return b
}

func (b *Builder) StringArray(name, description string, opts ...ParamOption) *Builder {
	b.param(name, "array", description, opts...)
	b.properties[name].(map[string]any)["items"] = map[string]any{"type": "string"}
	return b
}

func (b *Builder) ObjectArray(name, description string, properties map[string]any, required []string, opts ...ParamOption) *Builder {
	cfg := paramConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	items := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		items["required"] = append([]string(nil), required...)
	}
	property := map[string]any{"type": "array", "description": description, "items": items}
	b.properties[name] = property
	if cfg.required {
		b.required = appendUnique(b.required, name)
	}
	return b
}

func (b *Builder) BuildInfo() Info {
	info := b.info
	info.APITypes = append([]llm.APIType(nil), info.APITypes...)
	info.Tags = append([]string(nil), info.Tags...)
	info.DependsOn = append([]string(nil), info.DependsOn...)
	return info
}

func (b *Builder) Build(prepare func(context.Context, CallRequest, Info) (operationresult.Result[*PreparedCall], error)) ToolDefinition {
	info := b.BuildInfo()
	return ToolDefinition{Definition: info, Parameters: b.BuildSchema().Parameters, Prepare: func(ctx context.Context, req CallRequest) (operationresult.Result[*PreparedCall], error) {
		return prepare(ctx, req, info)
	}}
}

func (b *Builder) BuildSchema() llm.ToolSchema {
	parameters := map[string]any{
		"type":       "object",
		"properties": b.properties,
	}
	if len(b.required) > 0 {
		parameters["required"] = append([]string(nil), b.required...)
	}
	return llm.ToolSchema{
		Name:        b.info.Name,
		Description: b.info.Description,
		Parameters:  parameters,
	}
}

func (b *Builder) param(name, typ, description string, opts ...ParamOption) *Builder {
	cfg := paramConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	property := map[string]any{"type": typ, "description": description}
	if len(cfg.enum) > 0 {
		property["enum"] = cfg.enum
	}
	b.properties[name] = property
	if cfg.required {
		b.required = appendUnique(b.required, name)
	}
	return b
}

func Required() ParamOption {
	return func(cfg *paramConfig) { cfg.required = true }
}

func Enum(values ...string) ParamOption {
	return func(cfg *paramConfig) { cfg.enum = append([]string(nil), values...) }
}

func appendUnique(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}
