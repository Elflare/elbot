package elnis

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/events"
)

func TestPlatformCallFunctionReceivesResolvedPlatformAndContext(t *testing.T) {
	svc, close := newTestService(t, nil)
	defer close()
	targets := []Target{{Platform: "qqonebot"}, {Platform: "telegram"}}
	params := map[string]any{"message_id": 42, "nested": map[string]any{"values": []any{"id", true, nil}}}
	for _, platform := range []string{"", "telegram", " telegram ", "  "} {
		t.Run(platform, func(t *testing.T) {
			calls := 0
			svc.callPlatformAPI = func(ctx context.Context, got, api string, args map[string]any) (json.RawMessage, error) {
				calls++
				want := strings.TrimSpace(platform)
				if want == "" {
					want = targets[0].Platform
				}
				if ctx != t.Context() || got != want || api != " custom_api " || !reflect.DeepEqual(args, params) {
					t.Fatalf("forwarded %q %q %#v", got, api, args)
				}
				return json.RawMessage(`{}`), nil
			}
			result, err := svc.executeCalls(t.Context(), targets, []Call{{Platform: platform, API: " custom_api ", Params: params}})
			if err != nil || calls != 1 || result.ResultStatus != events.ResultSucceeded {
				t.Fatal(result, err, calls)
			}
		})
	}
}
func TestPlatformCallFunctionPreservesFailureAndCancellation(t *testing.T) {
	svc, close := newTestService(t, nil)
	defer close()
	for _, failure := range []error{errors.New("unsupported platform"), context.Canceled} {
		svc.callPlatformAPI = func(context.Context, string, string, map[string]any) (json.RawMessage, error) { return nil, failure }
		result, err := svc.executeCalls(t.Context(), nil, []Call{{Platform: "missing", API: "call"}})
		want := events.ResultFailed
		if errors.Is(failure, context.Canceled) {
			want = events.ResultCanceled
		}
		if !errors.Is(err, failure) || result.ResultStatus != want {
			t.Fatal(result, err)
		}
	}
}
