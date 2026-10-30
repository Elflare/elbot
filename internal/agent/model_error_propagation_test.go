package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"elbot/internal/config"
	"elbot/internal/events"
	"elbot/internal/signal"
)

func TestModelErrorReachesNotificationWithoutIntermediatePrefixes(t *testing.T) {
	for _, tc := range []struct {
		name, api, body, want string
		status                int
		cause                 error
	}{
		{
			name: "chat HTTP", api: "chat", status: http.StatusUnauthorized,
			body: `{"error":{"message":"invalid credentials"}}`,
			want: "HTTP 401: invalid credentials",
		},
		{
			name: "chat parse", api: "chat",
			body: "data: invalid-json\n\n",
			want: "parse stream chunk: invalid character 'i' looking for beginning of value",
		},
		{
			name: "chat EOF", api: "chat",
			body: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n",
			want: "chat stream closed before [DONE]: unexpected EOF", cause: io.ErrUnexpectedEOF,
		},
		{
			name: "response upstream", api: "response",
			body: "data: {\"type\":\"error\",\"error\":{\"code\":\"request_timeout\",\"message\":\"stream error: stream disconnected before completion: stream closed before response.completed\"}}\n\n",
			want: "request_timeout: stream error: stream disconnected before completion: stream closed before response.completed",
		},
		{
			name: "response EOF", api: "response",
			body: "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"status\":\"in_progress\"}}\n\n",
			want: "response stream closed before response.completed: unexpected EOF", cause: io.ErrUnexpectedEOF,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = io.WriteString(w, `{"data":[{"id":"m"}]}`)
					return
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				} else {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(server.Close)
			deps := isolatedModuleDeps(t)
			models, err := newConfiguredTestModels(t, testModelOptions{
				Providers:  map[string]config.ProviderConfig{"p": {BaseURL: server.URL, APIMode: tc.api, Models: []string{"m"}}},
				ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}, DefaultMode: "work",
			})
			if err != nil {
				t.Fatal(err)
			}
			deps.Models = models
			m, err := NewSupervisor(t.Context(), isolatedAgentConfig(), deps)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close(context.Background()) })
			if err := m.Start(); err != nil {
				t.Fatal(err)
			}
			var notices []string
			connection, err := events.NotificationRequested.Connect(func(_ context.Context, notice events.Notification) (events.NotificationReceipt, error) {
				if notice.Text != "barrier" {
					notices = append(notices, notice.Text)
				}
				return events.NotificationReceipt{}, nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = connection.Close(context.Background()) })
			err = m.HandleMessage(t.Context(), MessageRequest{Text: "hello"})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error=%v want=%q", err, tc.want)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("error cause lost: %v", err)
			}
			if _, err := events.NotificationRequested.Emit(t.Context(), events.Notification{Text: "barrier"}); err != nil {
				t.Fatal(err)
			}
			if len(notices) != 1 || notices[0] != "对话处理失败："+tc.want {
				t.Fatalf("notifications=%v", notices)
			}
		})
	}
}
