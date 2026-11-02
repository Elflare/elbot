package registry

import (
	"fmt"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/security"
	"elbot/internal/tool/share"
)

func BenchmarkCatalogQuery(b *testing.B) {
	for _, fields := range []int{1, 40} {
		b.Run(fmt.Sprintf("fields=%d", fields), func(b *testing.B) {
			ctx := contextinfo.WithActor(b.Context(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
			r := NewRegistry(security.DefaultPermission())
			d := (fakeTool{name: "bench", tags: []string{"files"}}).Definition()
			properties := d.Parameters["properties"].(map[string]any)
			for i := 0; i < fields; i++ {
				properties[fmt.Sprintf("field%d", i)] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
			}
			if _, err := r.RegisterTools(ctx, share.SourceBuiltin, share.ToolRegistrationScope{}, []share.ToolDefinition{d}); err != nil {
				b.Fatal(err)
			}
			b.Run("Get", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, ok := r.Get("bench"); !ok {
						b.Fatal("missing tool")
					}
				}
			})
			b.Run("Query", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if tools, err := r.Query(ctx, share.ToolQuery{}); err != nil || len(tools) != 1 {
						b.Fatal(tools, err)
					}
				}
			})
		})
	}
}

func BenchmarkNamedQuery(b *testing.B) {
	for _, count := range []int{1, 100, 10000} {
		b.Run(fmt.Sprintf("tools=%d", count), func(b *testing.B) {
			r := NewRegistry(security.DefaultPermission())
			definitions := make([]share.ToolDefinition, 0, count)
			for i := 0; i < count; i++ {
				definitions = append(definitions, (fakeTool{name: fmt.Sprintf("tool%d", i)}).Definition())
			}
			if _, err := r.RegisterTools(b.Context(), share.SourceBuiltin, share.ToolRegistrationScope{}, definitions); err != nil {
				b.Fatal(err)
			}
			for _, names := range [][]string{{"tool0"}, {}} {
				b.Run(fmt.Sprintf("names=%d", len(names)), func(b *testing.B) {
					query := share.ToolQuery{Scope: share.QueryCatalog, Names: names}
					b.ReportAllocs()
					for b.Loop() {
						if tools, err := r.Query(b.Context(), query); err != nil || len(tools) != len(names) {
							b.Fatal(tools, err)
						}
					}
				})
			}
		})
	}
}
