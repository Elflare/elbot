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
