package share

import "testing"

func TestAppendConfirmationTokens(t *testing.T) {
	for _, token := range []string{"y", " Y "} {
		if !IsAppendConfirm(token) {
			t.Fatalf("confirmation token %q", token)
		}
	}
	if IsAppendConfirm("more input") {
		t.Fatal("ordinary input interpreted as confirmation")
	}
}
