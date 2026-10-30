package input

import (
	"testing"

	"elbot/internal/command"
)

func TestCompactBlockedCommandSet(t *testing.T) {
	for _, effect := range []command.SessionEffect{command.SessionEffectSwitchCurrent, command.SessionEffectMutate, command.SessionEffectSwitchCurrent | command.SessionEffectMutate} {
		if !blocksDuringCompact(effect) {
			t.Fatalf("effect %d should be blocked", effect)
		}
	}
	if blocksDuringCompact(command.SessionEffectNone) {
		t.Fatal("commands without session effects should remain available")
	}
}

func testCommandText(name string, args ...string) string { return command.Format("/", name, args...) }
