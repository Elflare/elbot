package skill

import (
	"strings"
	"testing"
)

func TestParseSkillMarkdownFrontMatter(t *testing.T) {
	md := []byte("---\nname: docx\ndescription: \"Work with docx files\"\nwhen_to_use: Edit Word documents\nrisk: medium\n---\n\n# DOCX\n\nDetails here.")
	def, err := ParseSkillMarkdown(md, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "docx" {
		t.Fatalf("name = %q", def.Name)
	}
	if !strings.Contains(def.Description, "Work with docx files") || !strings.Contains(def.Description, "Edit Word documents") {
		t.Fatalf("description = %q", def.Description)
	}
	if def.Risk != "" {
		t.Fatalf("risk = %q", def.Risk)
	}
	if def.Detail != "# DOCX\n\nDetails here." {
		t.Fatalf("detail = %q", def.Detail)
	}
}

func TestParseSkillMarkdownFallbacks(t *testing.T) {
	md := []byte("# Title\n\nFirst useful paragraph.\n\nSecond paragraph.")
	def, err := ParseSkillMarkdown(md, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "demo" {
		t.Fatalf("name = %q", def.Name)
	}
	if def.Description != "Title" {
		t.Fatalf("description = %q", def.Description)
	}
}

func TestParseSkillMarkdownIgnoresRisk(t *testing.T) {
	def, err := ParseSkillMarkdown([]byte("---\nrisk: scary\n---\n\n# Demo"), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if def.Risk != "" {
		t.Fatalf("risk = %q", def.Risk)
	}
}
