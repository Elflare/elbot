// 构造工具元数据和参数 schema。
package share

import (
	"context"

	"elbot/internal/llm"
)

type Builder struct {
	needsElyphRules bool
	apiTypes        []llm.APIType
	name            string
	description     string
	risk            RiskLevel
	superadminOnly  bool
	hidden          bool
	ownerScoped     bool
	foregroundOnly  bool
	tags            []string
	dependsOn       []string
	properties      map[string]any
	required        []string
}

type ParamOption func(*paramConfig)

type paramConfig struct {
	required bool
	items    map[string]any
	enum     []string
}

func NewBuilder(name string) *Builder {
	return &Builder{name: name, properties: map[string]any{}}
}

func (b *Builder) Description(description string) *Builder {
	b.description = description
	return b
}

func (b *Builder) NeedsElyphRules() *Builder {
	b.needsElyphRules = true
	return b
}

func (b *Builder) Risk(risk RiskLevel) *Builder {
	b.risk = risk
	return b
}

func (b *Builder) Hidden() *Builder {
	b.hidden = true
	return b
}

func (b *Builder) Tags(tags ...string) *Builder {
	b.tags = append(b.tags, tags...)
	return b
}

func (b *Builder) SuperadminOnly() *Builder {
	b.superadminOnly = true
	return b
}

func (b *Builder) OwnerScoped() *Builder {
	b.ownerScoped = true
	return b
}

func (b *Builder) ForegroundOnly() *Builder {
	b.foregroundOnly = true
	return b
}

func (b *Builder) APITypes(types ...llm.APIType) *Builder {
	b.apiTypes = append([]llm.APIType(nil), types...)
	return b
}

func (b *Builder) DependsOn(names ...string) *Builder {
	b.dependsOn = append(b.dependsOn, names...)
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
	return b.param(name, "array", description, append(opts, Items("string"))...)
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
	return Info{Name: b.name, NeedsElyphRules: b.needsElyphRules, APITypes: append([]llm.APIType(nil), b.apiTypes...), Description: b.description, Risk: b.risk, SuperadminOnly: b.superadminOnly, Hidden: b.hidden, OwnerScoped: b.ownerScoped, ForegroundOnly: b.foregroundOnly, Tags: append([]string(nil), b.tags...), DependsOn: append([]string(nil), b.dependsOn...)}
}

func (b *Builder) Build(prepare func(context.Context, CallRequest, Info) (*PreparedCall, error)) ToolDefinition {
	info := b.BuildInfo()
	return ToolDefinition{Definition: info, Parameters: b.BuildSchema().Parameters, Prepare: func(ctx context.Context, req CallRequest) (*PreparedCall, error) {
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
		Name:        b.name,
		Description: b.description,
		Parameters:  parameters,
	}
}

func (b *Builder) param(name, typ, description string, opts ...ParamOption) *Builder {
	cfg := paramConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	property := map[string]any{"type": typ, "description": description}
	if cfg.items != nil {
		property["items"] = cfg.items
	}
	if len(cfg.enum) > 0 {
		property["enum"] = append([]string(nil), cfg.enum...)
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

func Items(typ string) ParamOption {
	return func(cfg *paramConfig) { cfg.items = map[string]any{"type": typ} }
}

func Enum(values ...string) ParamOption {
	return func(cfg *paramConfig) { cfg.enum = append([]string(nil), values...) }
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
