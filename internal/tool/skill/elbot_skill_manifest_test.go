package skill

import "testing"

func TestParseAgentSkillManifestValid(t *testing.T) {
	manifest, err := ParseAgentSkillManifest([]byte(`risk = "medium"
superadmin_only = true
tags = ["doc", "doc", "convert"]
command = ["python", "foo.py"]
timeout_seconds = 30
expose_root = true
parameters = '''
{"type":"object","required":["input"],"properties":{"input":{"type":"string"},"mode":{"type":"string"}}}
'''

[args]
input = "--input"
mode = "--mode"
`))
	if err != nil {
		t.Fatalf("ParseAgentSkillManifest: %v", err)
	}
	if manifest.Risk != "medium" || !manifest.SuperadminOnly || len(manifest.Command) != 2 || !manifest.ExposeRoot || manifest.Args["input"] != "--input" || !manifest.Callable {
		t.Fatalf("manifest = %#v", manifest)
	}
	if len(manifest.Tags) != 2 || manifest.Tags[0] != "doc" || manifest.Tags[1] != "convert" {
		t.Fatalf("tags = %#v", manifest.Tags)
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

func TestParseAgentSkillManifestRequiresRisk(t *testing.T) {
	_, err := ParseAgentSkillManifest([]byte(`command = ["python", "foo.py"]
parameters = '''{"type":"object","properties":{"input":{"type":"string"}}}'''
[args]
input = "--input"
`))
	if err == nil {
		t.Fatal("expected missing risk error")
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
