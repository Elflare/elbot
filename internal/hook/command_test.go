package hook

import (
	"reflect"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		want    []string
	}{
		{name: "plain", command: "uv run hook.py", want: []string{"uv", "run", "hook.py"}},
		{name: "quoted", command: `python "hook script.py" --name 'demo hook'`, want: []string{"python", "hook script.py", "--name", "demo hook"}},
		{name: "escaped", command: `run "a\\b\"c"`, want: []string{"run", `a\b"c`}},
		{name: "empty argument", command: `run ""`, want: []string{"run", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SplitCommand(tc.command)
			if err != nil {
				t.Fatalf("SplitCommand: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("SplitCommand = %#v, want %#v", got, tc.want)
			}
		})
	}
	if _, err := SplitCommand(`run "unterminated`); err == nil {
		t.Fatal("unterminated quote succeeded")
	}
}
