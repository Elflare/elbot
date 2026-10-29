package share

import "testing"

func TestAppendConfirmationTokens(t *testing.T) {
	for _, token := range []string{"$", "是", "y", "yes", " YES "} {
		if !IsAppendConfirm(token) || IsAppendCancel(token) {
			t.Fatalf("confirmation token %q", token)
		}
	}
	for _, token := range []string{"取消", "否", "n", "no", " NO "} {
		if !IsAppendCancel(token) || IsAppendConfirm(token) {
			t.Fatalf("cancellation token %q", token)
		}
	}
	if IsAppendConfirm("more input") || IsAppendCancel("more input") {
		t.Fatal("ordinary input interpreted as confirmation")
	}
}
