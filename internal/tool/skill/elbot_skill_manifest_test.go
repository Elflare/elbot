package skill

import "testing"

func TestParseAgentSkillManifestValid(t *testing.T) {
	manifest, err := ParseAgentSkillManifest([]byte(`risk = "medium"
superadmin_only = true
tags = ["doc", "doc", "convert"]
depends_on = ["read_file", "nls"]
command = ["python", "foo.py"]
timeout_seconds = 30
expose_root = true
use_workspace = true
parameters = '''
{"type":"object","required":["input"],"properties":{"input":{"type":"string"},"mode":{"type":"string"}}}
'''
positional = ["input"]

[args]
mode = "--mode"
`))
	if err != nil {
		t.Fatalf("ParseAgentSkillManifest: %v", err)
	}
	if manifest.Risk != "medium" || !manifest.SuperadminOnly || len(manifest.Command) != 2 || !manifest.ExposeRoot || !manifest.UseWorkspace || manifest.Args["mode"] != "--mode" || len(manifest.Positional) != 1 || manifest.Positional[0] != "input" || !manifest.Callable {
		t.Fatalf("manifest = %#v", manifest)
	}
	if len(manifest.Tags) != 3 || manifest.Tags[0] != "doc" || manifest.Tags[2] != "convert" {
		t.Fatalf("tags = %#v", manifest.Tags)
	}
	if len(manifest.DependsOn) != 2 || manifest.DependsOn[0] != "read_file" || manifest.DependsOn[1] != "nls" {
		t.Fatalf("depends_on = %#v", manifest.DependsOn)
	}
}

func TestParseAgentSkillManifestAllowsDocumentPolicyOnly(t *testing.T) {
	manifest, err := ParseAgentSkillManifest([]byte(`risk = "high"
superadmin_only = true
tags = ["private"]
`))
	if err != nil {
		t.Fatalf("ParseAgentSkillManifest: %v", err)
	}
	if manifest.Risk != "high" || !manifest.SuperadminOnly || manifest.Callable {
		t.Fatalf("manifest = %#v", manifest)
	}
	if len(manifest.Tags) != 1 || manifest.Tags[0] != "private" {
		t.Fatalf("tags = %#v", manifest.Tags)
	}
}

func TestParseAgentSkillManifestAllowsSuperadminOnlyWithoutRisk(t *testing.T) {
	manifest, err := ParseAgentSkillManifest([]byte(`superadmin_only = true
`))
	if err != nil {
		t.Fatalf("ParseAgentSkillManifest: %v", err)
	}
	if manifest.Risk != "safe" || !manifest.SuperadminOnly || manifest.Callable {
		t.Fatalf("manifest = %#v", manifest)
	}
}

func TestParseAgentSkillManifestLeavesRiskForRegistration(t *testing.T) {
	manifest, err := ParseAgentSkillManifest([]byte(`command = ["python", "foo.py"]
parameters = '''{"type":"object","properties":{"input":{"type":"string"}}}'''
[args]
input = "--input"
`))
	if err != nil || manifest.Risk != "" || manifest.TimeoutSeconds != 30 {
		t.Fatalf("manifest=%+v err=%v", manifest, err)
	}
}

func TestParseAgentSkillManifestRejectsUnknownField(t *testing.T) {
	_, err := ParseAgentSkillManifest([]byte(`risk = "low"
command = ["python", "foo.py"]
cwd = "."
parameters = '''{"type":"object","properties":{"input":{"type":"string"}}}'''
[args]
input = "--input"
`))
	if err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestParseAgentSkillManifestRejectsPositionalMapping(t *testing.T) {
	for name, positional := range map[string]string{
		"unknown":   `positional = ["missing"]`,
		"duplicate": `positional = ["input", "input"]`,
		"empty":     `positional = [""]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseAgentSkillManifest([]byte(`command = ["tool"]
parameters = '''{"type":"object","required":["input"],"properties":{"input":{"type":"string"}}}'''
` + positional))
			if err == nil {
				t.Fatal("expected positional mapping error")
			}
		})
	}
}

func TestParseAgentSkillManifestRejectsArgMissingProperty(t *testing.T) {
	_, err := ParseAgentSkillManifest([]byte(`risk = "low"
command = ["python", "foo.py"]
parameters = '''{"type":"object","required":["input"],"properties":{"input":{"type":"string"}}}'''
[args]
mode = "--mode"
`))
	if err == nil {
		t.Fatal("expected args/schema mismatch error")
	}
}
