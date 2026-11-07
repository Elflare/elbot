package cli

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestClientResultClassification(t *testing.T) {
	failure := errors.New("failure")
	for _, tt := range []struct {
		name, address string
		err           error
		want          ClientResultKind
	}{
		{want: ClientCompleted},
		{err: ErrClientNotConfigured, want: ClientFallback},
		{name: "local", err: ErrClientNotConfigured, want: ClientFailed},
		{address: "ws://127.0.0.1:1/cli/v1/ws", err: ErrServerUnavailable, want: ClientFallback},
		{address: "wss://[::1]:1/cli/v1/ws", err: ErrServerUnavailable, want: ClientFallback},
		{address: "ws://localhost:1/cli/v1/ws", err: ErrServerUnavailable, want: ClientFallback},
		{address: "ws://remote.example/cli/v1/ws", err: ErrServerUnavailable, want: ClientFailed},
		{address: "ws:///cli/v1/ws", err: ErrServerUnavailable, want: ClientFailed},
		{address: "http://localhost/cli/v1/ws", err: ErrServerUnavailable, want: ClientFailed},
		{name: "local", address: "ws://127.0.0.1:1", err: ErrServerUnavailable, want: ClientFailed},
		{err: failure, want: ClientFailed},
		{address: "ws://localhost:1", err: context.Canceled, want: ClientFailed},
		{address: "ws://localhost:1", err: context.DeadlineExceeded, want: ClientFailed},
	} {
		result := classifyClientResult(tt.name, tt.address, tt.err)
		if result.Kind != tt.want || result.Err != tt.err {
			t.Fatalf("%+v: %+v", tt, result)
		}
	}
}

func TestRunClientConfigurationAndDial(t *testing.T) {
	for _, name := range []string{"", "named"} {
		want := ClientFallback
		if name != "" {
			want = ClientFailed
		}
		for _, raw := range []map[string]any{nil, {"enabled": false}} {
			result := RunClient(t.Context(), raw, testConfigEnv(t.TempDir()), name)
			if result.Kind != want || !errors.Is(result.Err, ErrClientNotConfigured) {
				t.Fatal(result)
			}
		}
	}
	if result := RunClient(t.Context(), map[string]any{"enabled": "bad"}, testConfigEnv(t.TempDir()), ""); result.Kind != ClientFailed {
		t.Fatal(result)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	t.Setenv("ELBOT_CLI_DIAL_TOKEN", "secret")
	raw := map[string]any{"default_client": "local", "clients": map[string]any{"local": map[string]any{"url": "ws://" + address + defaultRemotePath, "token_env": []string{"ELBOT_CLI_DIAL_TOKEN"}}}}
	for _, name := range []string{"", "local"} {
		result := RunClient(t.Context(), raw, testConfigEnv(t.TempDir()), name)
		want := ClientFallback
		if name != "" {
			want = ClientFailed
		}
		if result.Kind != want || !errors.Is(result.Err, ErrServerUnavailable) {
			t.Fatal(result)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result := RunClient(ctx, raw, testConfigEnv(t.TempDir()), ""); result.Kind != ClientFailed || !errors.Is(result.Err, context.Canceled) {
		t.Fatal(result)
	}
}
