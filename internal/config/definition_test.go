package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDeclaredRequirementsAndExamples(t *testing.T) {
	definition := Definition{Rules: []Rule{
		{Path: []string{"required"}, Presence: Required, Children: []Rule{{Path: []string{"value"}, Presence: Required}}},
		{Path: []string{"optional"}, Children: []Rule{{Path: []string{"value"}, Presence: Required}}},
		{Path: []string{"examples"}, Example: true},
		{Path: []string{"required_example"}, Presence: Required, Example: true},
	}}
	const template = "ordinary = 1\nrequired_example = 'demo'\n[required]\nvalue = 'demo'\n[optional]\nvalue = 'demo'\n[examples.demo]\nvalue = 'demo'\n"
	for _, tc := range []struct {
		name, content string
		errors, hints int
		fields        []string
	}{
		{"missing parents", "", 2, 2, []string{"optional", "ordinary", "required", "required_example"}},
		{"required child", "[required]\n[optional]\nvalue='set'\nrequired_example='nested'\n", 2, 1, []string{"ordinary", "required.value", "required_example"}},
		{"empty required value", "required_example='set'\n[required]\nvalue=''\n", 1, 2, []string{"optional", "ordinary", "required.value"}},
		{"custom examples", "ordinary=2\nrequired_example='set'\n[required]\nvalue='set'\n[optional]\nvalue='set'\n[examples.mine]\nvalue='set'\n", 0, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := &document{path: "test.toml", data: []byte(tc.content)}
			decodeDocument(doc, nil)
			issues, err := inspectDocument(doc, definition, template)
			if err != nil {
				t.Fatal(err)
			}
			result := Inspection{}
			result.add(issues...)
			var fields []string
			errors, hints := 0, 0
			for _, issue := range result.Issues {
				fields = append(fields, keyPath(issue.Field))
				if issue.Level == LevelError {
					errors++
				} else {
					hints++
				}
			}
			if errors != tc.errors || hints != tc.hints || !reflect.DeepEqual(fields, tc.fields) {
				t.Fatalf("errors=%d hints=%d fields=%v issues=%+v", errors, hints, fields, result.Issues)
			}
		})
	}
}

func TestOptionalParentDoesNotRequireItsChildren(t *testing.T) {
	rules := []Rule{{Path: []string{"parent"}, Children: []Rule{{Path: []string{"child"}, Presence: Required}}}}
	if issues := CheckRules(map[string]any{}, rules); len(issues) != 0 {
		t.Fatalf("%+v", issues)
	}
	issues := CheckRules(map[string]any{"parent": map[string]any{}}, rules)
	if len(issues) != 1 || issues[0].Level != LevelError || keyPath(issues[0].Field) != "parent.child" {
		t.Fatalf("%+v", issues)
	}
}

func TestInspectionAndLoadShareDefaultsAndOverrides(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "custom.toml")
	files := map[string]string{
		"custom.toml":  "[config_files]\nproviders='vendor.toml'\nstate='choices.toml'\n[session]\ndefault_mode='chat'\n[mode_models.work]\nprovider='custom'\nmodel='main'\n[mode_models.chat]\nprovider='custom'\nmodel='chat'\n",
		"vendor.toml":  "[providers.custom]\nmodels=['main','state','chat']\n",
		"choices.toml": "[mode_models.work]\nprovider='custom'\nmodel='state'\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	inspection, err := NewInspector().Inspect(context.Background(), main)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(main)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inspection.Config, loaded) {
		t.Fatalf("inspection and startup differ:\n%+v\n%+v", inspection.Config, loaded)
	}
	if loaded.ModeModels["work"].Model != "state" || loaded.ModeModels["chat"].Model != "chat" || loaded.ModelMetadata.DefaultContextWindow != DefaultContextWindow {
		t.Fatalf("%+v", loaded)
	}
	state := newConfiguration(main, nil)
	if err := state.readCore(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	ctx := ValidationContext{Config: state.cfg, state: state}
	if ctx.Source("mode_models", "work", "model") != filepath.Join(dir, "choices.toml") || ctx.Source("mode_models", "chat", "model") != main {
		t.Fatal("wrong effective origins")
	}
}

func TestExplicitModuleDefinition(t *testing.T) {
	type module struct {
		Value string `toml:"value"`
	}
	for _, content := range []string{"value=123", "value='ok'\ntyop=true"} {
		t.Run(content, func(t *testing.T) {
			state := newConfiguration("app.toml", []Definition{{Asset: "app.toml", Node: []string{"platform", "custom"}, New: func() any { return &module{} }, UnknownFields: true}})
			doc := &document{path: "app.toml", data: []byte("[platform.custom]\n" + content)}
			decodeDocument(doc, func() any { return &Config{} })
			root, _ := state.definition("app.toml")
			issues := state.inspectModules(doc, root)
			if len(issues) != 1 || !strings.HasPrefix(keyPath(issues[0].Field), "platform.custom.") {
				t.Fatalf("%+v", issues)
			}
			if strings.Contains(content, "123") && issues[0].Level != LevelError {
				t.Fatalf("%+v", issues)
			}
			if strings.Contains(content, "tyop") && issues[0].Level != LevelHint {
				t.Fatalf("%+v", issues)
			}
		})
	}
}
