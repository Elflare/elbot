package tool

import "elbot/internal/llm"

type Builder struct {
	name           string
	description    string
	source         Source
	risk           RiskLevel
	superadminOnly bool
	hidden         bool
	dependsOn      []string
	properties     map[string]any
	required       []string
}

type ParamOption func(*paramConfig)

type paramConfig struct {
	required bool
	items    map[string]any
}

func NewBuilder(name string) *Builder {
	return &Builder{name: name, source: SourceBuiltin, risk: RiskLow, properties: map[string]any{}}
}

func (b *Builder) Description(description string) *Builder {
	b.description = description
	return b
}

func (b *Builder) Source(source Source) *Builder {
	b.source = source
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

func (b *Builder) SuperadminOnly() *Builder {
	b.superadminOnly = true
	return b
}

func (b *Builder) DependsOn(names ...string) *Builder {
	b.dependsOn = append(b.dependsOn, normalizeNames(names)...)
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
	return b.param(name, "object", description, opts...)
}

func (b *Builder) StringArray(name, description string, opts ...ParamOption) *Builder {
	return b.param(name, "array", description, append(opts, Items("string"))...)
}

func (b *Builder) BuildInfo() Info {
	return Info{Name: b.name, Description: b.description, Source: b.source, Risk: normalizeRisk(b.risk, RiskLow), SuperadminOnly: b.superadminOnly, Hidden: b.hidden, DependsOn: normalizeNames(b.dependsOn)}
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
		Type: "function",
		Function: llm.ToolFunctionSchema{
			Name:        b.name,
			Description: b.description,
			Parameters:  parameters,
		},
	}
}

func (b *Builder) param(name, typ, description string, opts ...ParamOption) *Builder {
	cfg := paramConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	property := map[string]any{"type": typ, "description": description}
	if cfg.items != nil {
		property["items"] = cfg.items
	}
	if b.properties == nil {
		b.properties = map[string]any{}
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

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
