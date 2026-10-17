package rules

import (
	"errors"
	"strings"
	"testing"

	"elbot/internal/hook"
)

func TestHookFailureTruncationMarker(t *testing.T) {
	body := strings.Repeat("雪🔥", 601)
	got := HookFailure(hook.Event{}, errors.New(body))
	if !strings.HasSuffix(got, strings.Repeat("雪🔥", 600)+"\n…") {
		t.Fatalf("notification = %q", got)
	}
}
