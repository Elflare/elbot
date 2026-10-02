package config

import (
	"strings"
	"testing"
)

func inspectTestDocument(t *testing.T, name, content, template string) []Issue {
	t.Helper()
	for _, definition := range CoreDefinitions() {
		if definition.Asset == name {
			state := newConfiguration("app.toml", nil)
			definition, _ = state.definition(name)
			doc := &document{path: name, data: []byte(content)}
			decodeDocument(doc, definition.New)
			issues, err := inspectDocument(doc, definition, template)
			if err != nil {
				t.Fatal(err)
			}
			return issues
		}
	}
	t.Fatalf("unknown definition %s", name)
	return nil
}

func TestProviderExamplesAreNotRequired(t *testing.T) {
	const metadata = "[model_metadata]\ndefault_context_window = 256000\n"
	var template string
	for _, asset := range DefaultAssets() {
		if asset.Path == "providers.toml" {
			template = asset.Content
		}
	}
	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{
		{name: "custom provider", content: "[providers.custom]\nbase_url = 'https://example.invalid'\n" + metadata},
		{name: "no providers", content: metadata},
		{name: "customized example providers", content: "[providers.deepseek]\nmodels = []\n[providers.openai]\nmodels = []\n" + metadata},
		{name: "missing metadata", content: "[providers.custom]\nmodels = []\n", want: "缺失节点：model_metadata；"},
		{name: "missing metadata field", content: "[model_metadata]\n", want: "缺失字段：model_metadata.default_context_window；"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := inspectTestDocument(t, "providers.toml", tc.content, template)
			if tc.want == "" {
				if len(issues) != 0 {
					t.Fatalf("%+v", issues)
				}
			} else if len(issues) != 1 || issues[0].Level != LevelHint || !strings.Contains(issues[0].Message, tc.want) {
				t.Fatalf("%+v", issues)
			}
		})
	}
}

func TestElnisErrorLocationSupportsTOMLSyntax(t *testing.T) {
	for _, tc := range []struct {
		name, file, content string
		wantLink            bool
	}{
		{"quoted slot", "state.toml", "[mode_models.'elwisp1']\r\nmodel = 123\r\n", true},
		{"dotted slot", "state.toml", "mode_models.elwisp2.model = 123\n", true},
		{"inline slot", "state.toml", "mode_models = { elwisp3 = { model = 123 } }\n", true},
		{"inline elnis", "app.toml", "elnis = { http = { workers = 'wrong' } }\n", true},
		{"inline path", "app.toml", "config_files = { elnis = 123 }\n", true},
		{"chat model", "state.toml", "[mode_models.chat]\nmodel = 123\n", false},
		{"multiline string", "state.toml", "ignored = '''\n[mode_models.elwisp1]\n'''\n[mode_models.chat]\nmodel = 123\n", false},
		{"custom tag", "tool_tags.toml", "[tags.elnis]\nprompt = 123\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := inspectTestDocument(t, tc.file, tc.content, "")
			if len(issues) != 1 || issues[0].Level != LevelError || (issues[0].Topic == TopicElnis) != tc.wantLink {
				t.Fatalf("%+v", issues)
			}
		})
	}
}
