package platform

import "testing"

func TestStripTriggerKeyword(t *testing.T) {
	got, ok := StripTriggerKeyword("芙莉丝你好", []string{"芙莉丝"})
	if !ok || got != "你好" {
		t.Fatalf("strip trigger = %q, %v", got, ok)
	}

	got, ok = StripTriggerKeyword("芙莉丝，你好", []string{"芙莉丝"})
	if !ok || got != "，你好" {
		t.Fatalf("strip trigger with punctuation = %q, %v", got, ok)
	}

	got, ok = StripTriggerKeyword("你好芙莉丝", []string{"芙莉丝"})
	if ok || got != "你好芙莉丝" {
		t.Fatalf("non-prefix trigger = %q, %v", got, ok)
	}
}
