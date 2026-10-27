package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/platform/share"
)

type apiSender struct {
	testSender
	name string
	call func(context.Context, string, map[string]any) (json.RawMessage, error)
}

func (s apiSender) Name() string { return s.name }
func (s apiSender) CallPlatformAPI(ctx context.Context, api string, params map[string]any) (json.RawMessage, error) {
	return s.call(ctx, api, params)
}

func TestNativeCallRoutesExplicitPlatformAndPreservesValues(t *testing.T) {
	primary := apiSender{name: "primary", call: func(context.Context, string, map[string]any) (json.RawMessage, error) {
		t.Fatal("called default or context platform")
		return nil, nil
	}}
	router := New(Options{Primary: primary})
	ctx := share.WithMessageContext(context.Background(), share.MessageContext{Sender: primary})
	ctx = contextinfo.WithConversation(ctx, contextinfo.Conversation{Source: contextinfo.Source{Platform: "primary"}})
	params := map[string]any{"id": json.Number("9007199254740993"), "nested": map[string]any{"value": "unchanged"}}
	want := json.RawMessage("{ \"native\" : true }")
	wantErr := errors.New("native failure")
	calls := 0
	router.RegisterPlatform(apiSender{name: "other", call: func(got context.Context, api string, values map[string]any) (json.RawMessage, error) {
		calls++
		if got != ctx || api != " custom.api " || !reflect.DeepEqual(values, params) {
			t.Fatalf("changed arguments: %v %q %#v", got, api, values)
		}
		return want, wantErr
	}})
	result, err := router.CallPlatformAPI(ctx, " other ", " custom.api ", params)
	if err != wantErr || string(result) != string(want) || calls != 1 {
		t.Fatalf("result=%s err=%v calls=%d", result, err, calls)
	}
	for _, name := range []string{"", " ", "missing"} {
		if _, err := router.CallPlatformAPI(ctx, name, "api", nil); err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("platform %q: %v", name, err)
		}
	}
	router.RegisterPlatform(testSender{})
	if _, err := router.CallPlatformAPI(ctx, "primary", "api", nil); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("unsupported platform: %v", err)
	}
}

func TestNativeCallReleasesRegistryLockAndPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	result := make(chan error, 1)
	router := New(Options{Primary: apiSender{name: "primary", call: func(got context.Context, _ string, _ map[string]any) (json.RawMessage, error) {
		close(entered)
		<-got.Done()
		return nil, got.Err()
	}}})
	go func() {
		_, err := router.CallPlatformAPI(ctx, "primary", "api", nil)
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("call did not start")
	}
	registered := make(chan struct{})
	go func() {
		router.RegisterPlatform(testSender{})
		close(registered)
	}()
	select {
	case <-registered:
	case <-time.After(5 * time.Second):
		t.Fatal("native call holds registry lock")
	}
	cancel()
	select {
	case err := <-result:
		if err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not reach adapter")
	}
}
