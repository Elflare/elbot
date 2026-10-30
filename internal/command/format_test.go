package command

import "testing"

func TestFormatPreservesCommandArguments(t *testing.T) {
	for _, tc := range []struct {
		prefix, name string
		args         []string
		want         string
	}{
		{"!", "stop", nil, "!stop"},
		{"!!", "resume", []string{"--page", "2", ""}, "!!resume --page 2"},
		{"！", "rename", []string{"sess_123", "标题 / 含空格"}, "！rename sess_123 标题 / 含空格"},
	} {
		if got := Format(tc.prefix, tc.name, tc.args...); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}
