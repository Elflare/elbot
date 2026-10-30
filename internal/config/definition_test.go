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

func TestRuleLookupInheritanceAndDeclarationOrder(t *testing.T) {
	rules := []Rule{
		{Path: []string{"parent"}, Presence: Required, Example: true, Topic: "parent", Hint: "parent hint", Children: []Rule{
			{Path: []string{"child"}, Presence: Required, Effective: true, Topic: "child", Hint: "child hint"},
		}},
		{Path: []string{"parent", "child"}, Topic: "later"},
		{Path: []string{"other"}, Topic: "unrelated"},
	}
	for _, tc := range []struct {
		path []string
		want Rule
	}{
		{[]string{"parent"}, Rule{Presence: Required, Example: true, Topic: "parent", Hint: "parent hint"}},
		{[]string{"parent", "child"}, Rule{Example: true, Effective: true, Topic: "later", Hint: "child hint"}},
		{[]string{"parent", "child", "leaf"}, Rule{Example: true, Effective: true, Topic: "later", Hint: "child hint"}},
		{[]string{"parent", "sibling"}, Rule{Example: true, Topic: "parent", Hint: "parent hint"}},
		{[]string{"parent.child"}, Rule{}},
	} {
		t.Run(keyPath(tc.path), func(t *testing.T) {
			if got := ruleAt(rules, tc.path); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("rule = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCheckRulesBuildsFullPathsAndInheritsTopics(t *testing.T) {
	rules := []Rule{{Path: []string{"a.b"}, Topic: "parent", Children: []Rule{
		{Path: []string{`quoted"key`}, Presence: Required},
		{Path: []string{"optional"}, Hint: "optional hint", Topic: "child"},
		{Path: []string{"sibling"}, Presence: Required},
	}}}
	value := map[string]any{"a.b": map[string]any{`quoted"key`: ""}}
	want := []Issue{
		{Field: []string{"a.b", `quoted"key`}, Missing: true, Level: LevelError, Topic: "parent", Message: `有效配置缺少 "a.b"."quoted\"key"`},
		{Field: []string{"a.b", "optional"}, Missing: true, Level: LevelHint, Topic: "child", Message: `缺失可选配置："a.b".optional；optional hint`},
		{Field: []string{"a.b", "sibling"}, Missing: true, Level: LevelError, Topic: "parent", Message: `有效配置缺少 "a.b".sibling`},
	}
	if got := CheckRules(value, rules); !reflect.DeepEqual(got, want) {
		t.Fatalf("issues = %+v, want %+v", got, want)
	}
}

func TestInspectionAndLoadShareDefaultsAndOverrides(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "custom.toml")
	files := map[string]string{
		"custom.toml":  "[config_files]\nproviders='vendor.toml'\nstate='choices.toml'\n[session]\ndefault_mode='chat'\n[naming_model]\nprovider='custom'\nmodel='title'\n[mode_models.work]\nprovider='custom'\nmodel='main'\n[mode_models.chat]\nprovider='custom'\nmodel='chat'\n",
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
	if loaded.Session.DefaultMode != "chat" || loaded.NamingModel != (ModelSelection{Provider: "custom", Model: "title"}) {
		t.Fatal("omitted state fields replaced main configuration")
	}
	for _, tc := range []struct {
		name, content string
		selection     ModelSelection
	}{
		{"provider only", "provider='custom'\n", ModelSelection{Provider: "custom"}},
		{"model only", "model='state'\n", ModelSelection{Model: "state"}},
		{"empty selection", "", ModelSelection{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "choices.toml")
			content := "[session]\ndefault_mode='work'\n[mode_models.work]\n" + tc.content + "[naming_model]\n" + tc.content + "[compact_model]\n" + tc.content
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			state := newConfiguration(main, nil)
			if err := state.readCore(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			wantNaming, wantSource := tc.selection, path
			if tc.selection == (ModelSelection{}) {
				wantNaming, wantSource = loaded.NamingModel, main
			}
			cfg := state.cfg
			if cfg.ModeModels["work"] != tc.selection || cfg.ModeModels["chat"] != loaded.ModeModels["chat"] || cfg.NamingModel != wantNaming || cfg.CompactModel != tc.selection || cfg.Session.DefaultMode != "work" {
				t.Fatalf("unexpected state overrides: %+v", cfg)
			}
			ctx := ValidationContext{Config: cfg, state: state}
			if ctx.Source("mode_models", "work", "model") != path || ctx.Source("mode_models", "chat", "model") != main || ctx.Source("session", "default_mode") != path || ctx.Source("naming_model", "model") != wantSource || ctx.Source("compact_model", "provider") != wantSource {
				t.Fatal("origins disagree with state overrides")
			}
			inspection, err := NewInspector().Inspect(context.Background(), main)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, issue := range inspection.Issues {
				if issue.Level == LevelError && strings.HasPrefix(keyPath(issue.Field), "mode_models.work.") {
					found = true
					if issue.Path != path {
						t.Fatalf("partial model error attributed to wrong document: %+v", issue)
					}
				}
			}
			if !found {
				t.Fatal("partial model override did not produce a validation error")
			}
		})
	}
}

func TestAssetPathDefaultsAndInvalidReferences(t *testing.T) {
	main := filepath.Join(t.TempDir(), "app.toml")
	for _, asset := range DefaultAssets() {
		if len(asset.Reference) == 0 {
			continue
		}
		for _, tc := range []struct {
			name, content, path string
		}{
			{"missing parent", "", asset.Path},
			{"missing field", "[" + asset.Reference[0] + "]", asset.Path},
			{"empty", keyPath(asset.Reference) + "=''", asset.Path},
			{"relative", keyPath(asset.Reference) + "='custom/file'", "custom/file"},
			{"absolute", keyPath(asset.Reference) + "='" + main + "'", main},
			{"invalid parent", asset.Reference[0] + "=123", ""},
			{"invalid field", keyPath(asset.Reference) + "=123", ""},
			{"invalid TOML", "[", ""},
		} {
			t.Run(asset.Path+"/"+tc.name, func(t *testing.T) {
				state := newConfiguration(main, nil)
				doc := &document{data: []byte(tc.content)}
				decodeDocument(doc, nil)
				state.documents[state.root] = doc
				path, err := state.assetPath(asset.Path)
				if tc.path == "" {
					if err == nil || path != "" {
						t.Fatalf("guessed dependent path: %q, %v", path, err)
					}
				} else if err != nil || path != resolveRelative(main, tc.path) {
					t.Fatalf("path = %q, err = %v; want %q", path, err, tc.path)
				}
			})
		}
	}
}

func TestExplicitModuleDefinition(t *testing.T) {
	type module struct {
		Value string `toml:"value"`
	}
	for _, tc := range []struct {
		name, content string
		strict        bool
		fields        []string
		level         Level
	}{
		{"valid", "value='ok'", true, nil, ""},
		{"type", "value=123", true, []string{"value"}, LevelError},
		{"unknown", "value='ok'\ntyop=true", true, []string{"tyop"}, LevelHint},
		{"sorted unknown fields", "value='ok'\nz=true\n'a.b'=1", true, []string{"a.b", "z"}, LevelHint},
		{"type before unknown hints", "aaa=true\nvalue=123\nzzz=1", true, []string{"value"}, LevelError},
		{"unknown allowed", "value='ok'\ntyop=true", false, nil, ""},
		{"type still checked", "value=123\ntyop=true", false, []string{"value"}, LevelError},
		{"required checked once", "", true, []string{"value"}, LevelError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := newConfiguration("app.toml", []Definition{{
				Asset: "app.toml", Node: []string{"platform", "custom"}, Topic: "module",
				New: func() any { return &module{} }, UnknownFields: tc.strict,
				Rules: []Rule{{Path: []string{"value"}, Presence: Required, Topic: "value"}},
			}})
			doc := &document{path: "app.toml", data: []byte("[platform.custom]\n" + tc.content)}
			decodeDocument(doc, func() any { return &Config{} })
			root, _ := state.definition("app.toml")
			issues, err := inspectDocument(doc, root, "")
			if err != nil {
				t.Fatal(err)
			}
			// Inspect both stages before deduplication so repeated checks are visible.
			issues = append(issues, state.inspectModules(doc, root)...)
			if len(issues) != len(tc.fields) {
				t.Fatalf("%+v", issues)
			}
			for i, name := range tc.fields {
				field, topic := []string{"platform", "custom", name}, "module"
				if name == "value" {
					topic = "value"
				}
				issue := issues[i]
				if !reflect.DeepEqual(issue.Field, field) || issue.Path != doc.path || issue.Level != tc.level || issue.Topic != topic || !strings.Contains(issue.Message, keyPath(field)) || strings.Contains(issue.Message, "第 ") {
					t.Fatalf("unexpected module diagnostic: %+v", issue)
				}
			}
		})
	}
}
