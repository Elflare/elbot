package rules

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/hook"
	hookruntime "elbot/internal/hook/runtime"
	"elbot/internal/media"
	"elbot/internal/storage/sqlite"
)

func TestExecMediaProtocolRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	runtime := hookruntime.NewManager(hookruntime.Options{Media: center})
	defer runtime.Close(ctx)
	module := Module{Opts: Options{Runtime: runtime}}
	got, err := module.runRule(ctx, Rule{Actions: []Action{{Type: "exec", Command: execHelperCommand("media")}}}, hook.Event{Point: hook.PointAgentInputPrepared})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Message.Segments) != 1 || !media.ValidID(got.Message.Segments[0].MediaID) || got.Message.Segments[0].URL != "" {
		t.Fatalf("message=%#v", got.Message)
	}
	if len(got.Outputs) != 1 || got.Outputs[0].Source.MediaID != got.Message.Segments[0].MediaID {
		t.Fatalf("outputs=%#v", got.Outputs)
	}
}

func TestRuleSendMediaID(t *testing.T) {
	id := "media:" + strings.Repeat("a", 64)
	for _, action := range []Action{
		{Type: "send", Kind: "image", URL: id},
		{Type: "send", Outputs: []SegmentSpec{{Kind: "image", URL: id}}},
	} {
		got, err := (Module{}).runRule(context.Background(), Rule{Actions: []Action{action}}, hook.Event{})
		if err != nil || len(got.Outputs) != 1 || got.Outputs[0].Source.MediaID != id {
			t.Fatalf("got=%#v err=%v", got, err)
		}
	}
	if _, err := makeOutputs(Action{URL: id, Outputs: []SegmentSpec{{Kind: "text", Text: "mixed"}}}, hook.Event{}, state{}); err == nil {
		t.Fatal("mixed quick fields accepted")
	}
}
