// 规范化、格式化并验证 Go Skill 源码与模块声明。
package skill

import (
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
)

func normalizeGoSource(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.TrimRight(text, "\n") + "\n"
}

func formatGoSource(source string) (string, error) {
	formatted, err := format.Source([]byte(normalizeGoSource(source)))
	if err != nil {
		return "", fmt.Errorf("gofmt source: %w", err)
	}
	return string(formatted), nil
}

func validateGoMainSource(source string) error {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", source, parser.AllErrors)
	if err != nil {
		return fmt.Errorf("parse Go source: %w", err)
	}
	if file.Name.Name != "main" {
		return fmt.Errorf("go source must declare package main")
	}
	return nil
}

func parseGoModVersion(output string) (string, error) {
	version := strings.TrimSpace(output)
	if !strings.HasPrefix(version, "go") {
		return "", fmt.Errorf("detect go version: unsupported go version output %q", version)
	}
	version = strings.TrimPrefix(version, "go")
	parts := strings.Split(version, ".")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("detect go version: unsupported go version output %q", strings.TrimSpace(output))
	}
	minor := parts[1]
	for i, r := range minor {
		if r < '0' || r > '9' {
			minor = minor[:i]
			break
		}
	}
	if minor == "" {
		return "", fmt.Errorf("detect go version: unsupported go version output %q", strings.TrimSpace(output))
	}
	return parts[0] + "." + minor, nil
}

func goModContent(name, version string) string {
	return fmt.Sprintf("module elbot-skill/%s\n\ngo %s\n", name, version)
}

func validateGoSource(source string) error {
	if !strings.Contains(source, "package main") {
		return fmt.Errorf("go_source must contain package main")
	}
	return nil
}
