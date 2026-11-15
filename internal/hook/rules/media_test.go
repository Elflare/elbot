// 验证一次性插件的媒体交互和规则媒体输出。

package rules

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	hookruntime "elbot/internal/hook/runtime"
	"elbot/internal/hook/share"
	"elbot/internal/media"
	"elbot/internal/operationresult"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil/mediatest"
)

func TestExecMediaProtocolRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	center := mediatest.New(t, media.MediaCenterDeps{Store: store}, root)
	runtime, err := hookruntime.NewManager(hookruntime.Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir(), Media: center})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(ctx)
	module := Module{Opts: Options{Log: testLog, Notify: testNotify, Runtime: runtime, BuildMessageSegments: runtime.BuildMessageSegments}}
	got, err := module.runRule(ctx, Rule{Actions: []Action{{Type: "exec", Command: execHelperCommand("media")}}}, share.Event{Point: share.PointAgentInputPrepared}, false)
	if err != nil || got.Status != operationresult.Success {
		t.Fatal(err)
	}
	if len(got.Data.Message.Segments) != 1 || !media.ValidID(got.Data.Message.Segments[0].MediaID) || got.Data.Message.Segments[0].URL != "" {
		t.Fatalf("message=%#v", got.Data.Message)
	}
	if len(got.Data.Outputs) != 1 || got.Data.Outputs[0].Source.MediaID != got.Data.Message.Segments[0].MediaID {
		t.Fatalf("outputs=%#v", got.Data.Outputs)
	}
}

func TestRuleSendMediaID(t *testing.T) {
	id := "media:" + strings.Repeat("a", 64)
	for _, action := range []Action{
		{Type: "send", Kind: "image", URL: id},
		{Type: "send", Outputs: []SegmentSpec{{Kind: "image", URL: id}}},
	} {
		got, err := (Module{Opts: Options{Log: testLog, Notify: testNotify}}).runRule(context.Background(), Rule{Actions: []Action{action}}, share.Event{}, false)
		if err != nil || got.Status != operationresult.Success || len(got.Data.Outputs) != 1 || got.Data.Outputs[0].Source.MediaID != id {
			t.Fatalf("got=%#v err=%v", got, err)
		}
	}
	if _, err := makeOutputs(Action{URL: id, Outputs: []SegmentSpec{{Kind: "text", Text: "mixed"}}}, share.Event{}, state{}); err == nil {
		t.Fatal("mixed quick fields accepted")
	}
}
