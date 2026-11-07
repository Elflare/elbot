package share

import (
	"strings"
	"testing"

	command "elbot/internal/command/share"
)

func TestFormatCommandResultRendersData(t *testing.T) {
	tests := []struct {
		name string
		kind command.ResultKind
		data any
		want string
	}{
		{name: "text", kind: command.ResultKindText, data: command.TextData{Content: "accepted"}, want: "accepted"},
		{name: "session change", data: command.SessionChangeData{Operation: "fork", Session: &command.SessionData{ID: "child", Mode: "chat", ParentSessionID: "parent", ForkFromMessageID: "message"}}, want: "fork → child · chat\nparent=parent · from=message"},
		{name: "status", data: command.StatusData{Mode: "chat", Model: command.ModelData{Provider: "p", Model: "m"}}, want: "session=nil\ndefault=chat · model=p/m"},
		{name: "doctor", data: command.DoctorData{}, want: "Everything is OK"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatCommandResult(&command.Result{Kind: tt.kind, Data: tt.data})
			if !strings.Contains(got, tt.want) {
				t.Fatalf("FormatCommandResult() = %q, want to contain %q", got, tt.want)
			}
		})
	}
}

func TestMessagePreviewsIncludeAttachments(t *testing.T) {
	for _, body := range []string{"", "body"} {
		message := command.MessageData{Role: "user", Content: body, Attachments: []command.AttachmentData{{Name: "note.txt"}, {Name: "image.png"}}}
		for _, data := range []any{
			command.MessagesData{Items: []command.MessageData{message}},
			command.SessionsData{Items: []command.SessionSummaryData{{Messages: []command.MessageData{message}}}},
			command.SessionChangeData{Operation: "resume", Session: &command.SessionData{ID: "s"}, Messages: []command.MessageData{message}},
			command.SessionChangeData{Operation: "fork", Session: &command.SessionData{ID: "s"}, Messages: []command.MessageData{message}},
		} {
			text := FormatCommandResult(&command.Result{Data: data})
			want := "user: "
			if body != "" {
				want += body + " "
			}
			want += "[附件: note.txt] [附件: image.png]"
			if !strings.Contains(text, want) {
				t.Fatalf("%T preview=%q, want %q", data, text, want)
			}
		}
	}
}

func TestSessionsPreviewKeepsNewestMessages(t *testing.T) {
	d := command.SessionsData{Items: []command.SessionSummaryData{{SessionData: command.SessionData{ID: "s-id", Title: "title"}, Messages: []command.MessageData{
		{Role: "user", Content: "omitted"}, {Role: "assistant", Content: "second"},
		{Role: "user", Content: "third"}, {Role: "assistant", Content: "fourth"}, {Role: "user", Content: "latest"},
	}}}}
	text := FormatCommandResult(&command.Result{Data: d})
	if !strings.HasPrefix(text, "[s-id] title\n") || strings.Count(text, "s-id") != 1 || strings.Contains(text, "omitted") || !strings.Contains(text, "... / bot: second / user: third / bot: fourth / user: latest") {
		t.Fatalf("sessions preview=%q", text)
	}
}

func TestDamagedLogsReportHiddenSamples(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
		want  string
	}{
		{"all", []string{"one", "two", "three"}, ""},
		{"partial", []string{"one"}, "damaged hidden=2"},
		{"none", nil, "damaged hidden=3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := FormatCommandResult(&command.Result{Data: command.LogsData{DamagedCount: 3, DamagedLines: tc.lines}})
			if !strings.Contains(text, "damaged=3") || (tc.want == "" && strings.Contains(text, "damaged hidden=")) || (tc.want != "" && !strings.Contains(text, tc.want)) {
				t.Fatal(text)
			}
			for _, line := range tc.lines {
				if !strings.Contains(text, line) {
					t.Fatalf("missing sample %q: %s", line, text)
				}
			}
		})
	}
}

func TestEmptyUsageRetainsNotices(t *testing.T) {
	for _, tc := range []struct {
		data command.UsageData
		want string
	}{
		{command.UsageData{}, "-"},
		{command.UsageData{RangeTruncated: true, AvailableCalls: 3, DamagedCount: 2}, "-\n仅有 3 次调用\nwarn: damaged=2 · usage incomplete"},
	} {
		if got := FormatCommandResult(&command.Result{Data: tc.data}); got != tc.want {
			t.Fatalf("usage=%q, want %q", got, tc.want)
		}
	}
	text := FormatCommandResult(&command.Result{Data: command.UsageData{Total: command.UsageBucket{Calls: 1}}})
	if !strings.Contains(text, "calls: 1") || !strings.Contains(text, "prompt: unk") {
		t.Fatal(text)
	}
}
