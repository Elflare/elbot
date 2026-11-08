package builtin

import (
	"strings"
	"testing"
)

func TestUsageQueryOptionsAndRanges(t *testing.T) {
	query, group, callRange, err := parseUsageQuery("-n 2-4 -d 7 -m gpt -s s1 --by day --since 2h")
	if err != nil || callRange == nil || callRange.start != 2 || callRange.end != 4 || query.Limit != 4 || query.Days != 7 || query.ContentFields["model"] != "gpt" || query.Fields["session_id"] != "s1" || group != groupByDay || query.Since == nil {
		t.Fatalf("query=%+v group=%s range=%+v err=%v", query, group, callRange, err)
	}
	for _, args := range []string{"-n 0", "-n 3-1", "-n 100001", "--by other"} {
		if _, _, _, err := parseUsageQuery(args); err == nil {
			t.Fatalf("parseUsageQuery(%q) succeeded", args)
		}
	}
	if !strings.Contains(usageInfo().Help, "--by") {
		t.Fatal("usage help missing group option")
	}
}
