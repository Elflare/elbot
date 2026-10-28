package share

import (
	"strings"
	"testing"
)

func TestValidID(t *testing.T) {
	hash := strings.Repeat("0123456789abcdef", 4)
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"valid", IDPrefix + hash, true},
		{"empty", "", false},
		{"missing hash", IDPrefix, false},
		{"short", IDPrefix + hash[:63], false},
		{"long", IDPrefix + hash + "0", false},
		{"wrong prefix", "image:" + hash, false},
		{"uppercase prefix", "MEDIA:" + hash, false},
		{"uppercase hash", IDPrefix + strings.ToUpper(hash), false},
		{"non hex", IDPrefix + hash[:63] + "g", false},
		{"unicode", IDPrefix + hash[:61] + "图", false},
		{"leading whitespace", " " + IDPrefix + hash, false},
		{"trailing whitespace", IDPrefix + hash + "\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidID(tt.id); got != tt.want {
				t.Fatalf("ValidID(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"report.txt", "report.txt"},
		{"  报告.txt \n", "报告.txt"},
		{"/tmp/reports/report.txt", "report.txt"},
		{`C:\reports\report.txt`, "report.txt"},
		{`/tmp\reports/report.txt`, "report.txt"},
		{"https://example.test/reports/report.txt?token=secret#page", "report.txt"},
		{"HTTP://example.test/report.txt?download=1", "report.txt"},
		{"file:///tmp/report.txt", "report.txt"},
		{"https://example.test", "file"},
		{"https://example.test/", "file"},
		{"%E6%8A%A5%E5%91%8A.txt", "报告.txt"},
		{"folder%2Freport.txt", "report.txt"},
		{"folder%5Creport.txt", "report.txt"},
		{"https://example.test/folder%2Freport%20one.txt?token=secret", "report one.txt"},
		{"folder%252Freport.txt", "folder%2Freport.txt"},
		{"https://example.test/folder%252Freport.txt", "folder%2Freport.txt"},
		{"https://example.test/folder%5Creport.txt", "report.txt"},
		{"https://example.test/%E6%8A%A5%E5%91%8A.txt?token=secret", "报告.txt"},
		{"https://example.test/report%25ZZ.txt?token=secret", "report%ZZ.txt"},
		{"http://example.test/report%ZZ.txt?token=secret", "file"},
		{"HtTpS://example.test/report%ZZ.txt?token=secret", "file"},
		{"FiLe:///tmp/report%ZZ.txt?token=secret", "file"},
		{"http://example.test/report.txt?token=secret#%ZZ", "file"},
		{"HTTPS://example.test/report.txt?token=secret#%ZZ", "file"},
		{"file:///tmp/report.txt?token=secret#%ZZ", "file"},
		{"bad%ZZ.txt", "bad%ZZ.txt"},
		{"", "file"},
		{" \t\n", "file"},
		{".", "file"},
		{"..", "file"},
		{"/", "file"},
		{`\`, "file"},
		{"%2E%2E", "file"},
		{"%2F", "file"},
		{"data:image/png;base64,abc", "file"},
		{" DaTa:text/plain,abc ", "file"},
		{"base64:abc", "file"},
		{" BASE64:abc ", "file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeName(tt.name); got != tt.want {
				t.Fatalf("SanitizeName(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}
