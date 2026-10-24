package share

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateLogContentUnicodeBoundaries(t *testing.T) {
	for _, limit := range []int{512, 1024} {
		for _, length := range []int{0, limit - 1, limit, limit + 1} {
			text := strings.Repeat("🔥", length)
			got := TruncateLogContent(text, limit)
			if !utf8.ValidString(got) || utf8.RuneCountInString(got) != min(length, limit) {
				t.Fatalf("limit=%d length=%d: invalid Unicode or length", limit, length)
			}
			if length <= limit && got != text {
				t.Fatal("short text was changed")
			}
			if length > limit && got != strings.Repeat("🔥", limit-1)+"…" {
				t.Fatal("truncated prefix or marker changed")
			}
		}
	}
}
