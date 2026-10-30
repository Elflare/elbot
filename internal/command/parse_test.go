package command

import (
	"context"
	"slices"
	"testing"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		prefixes   []string
		want       Parsed
	}{
		{name: "longest", text: "!!fork abc", prefixes: []string{"!", "!!"}, want: Parsed{OK: true, Prefix: "!!", Name: "fork", Args: "abc"}},
		{name: "reverse order", text: "!!fork", prefixes: []string{"!!", "!"}, want: Parsed{OK: true, Prefix: "!!", Name: "fork"}},
		{name: "short prefix", text: "!help", prefixes: []string{"!", "!!"}, want: Parsed{OK: true, Prefix: "!", Name: "help"}},
		{name: "case and whitespace", text: "  !!FoRK   abc  def  ", prefixes: []string{"!", "!!"}, want: Parsed{OK: true, Prefix: "!!", Name: "fork", Args: "abc  def"}},
		{name: "unicode", text: "！！fork", prefixes: []string{"！", "！！"}, want: Parsed{OK: true, Prefix: "！！", Name: "fork"}},
		{name: "prefix only", text: "!!", prefixes: []string{"!", "!!"}, want: Parsed{OK: true, Prefix: "!!"}},
		{name: "extra slash", text: "!/help", prefixes: []string{"!"}, want: Parsed{OK: true, Prefix: "!", Name: "/help"}},
		{name: "extra slash default prefix", text: "//help", prefixes: []string{"/"}, want: Parsed{OK: true, Prefix: "/", Name: "/help"}},
		{name: "plain text", text: "hello", prefixes: []string{"!", "!!"}},
		{name: "unconfigured slash", text: "/help", prefixes: []string{"!"}},
		{name: "empty", text: " ", prefixes: []string{"!"}},
		{name: "no prefixes", text: "/help"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.prefixes)
			if got := Parse(tc.text, tc.prefixes); got != tc.want {
				t.Fatalf("Parse(%q) = %#v, want %#v", tc.text, got, tc.want)
			}
			if !slices.Equal(tc.prefixes, before) {
				t.Fatalf("prefixes changed: %q", tc.prefixes)
			}
		})
	}
}

func TestRouterDoesNotStripExtraSlash(t *testing.T) {
	r := NewRouter([]string{"!", "!!"})
	if err := r.Register(NewFunc(Info{Name: "help"}, func(_ context.Context, _ Request) (*Result, error) {
		t.Fatal("extra slash dispatched to help")
		return nil, nil
	})); err != nil {
		t.Fatal(err)
	}
	result, err := r.Dispatch(t.Context(), "!/help")
	if err != nil || result.Content != "unk command: !/help\n!help" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertComplete(t, r.Complete("!/he"), nil)
	assertComplete(t, r.Complete("!!he"), []string{"!!help"})
}
