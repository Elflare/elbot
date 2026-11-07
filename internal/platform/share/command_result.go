package share

import (
	"fmt"

	command "elbot/internal/command/share"
)

// FormatSimpleCommandResult returns only command results whose display is a
// single plain-text value shared by every platform.
func FormatSimpleCommandResult(result *command.Result) string {
	if result == nil {
		return ""
	}
	switch data := result.Data.(type) {
	case command.TextData:
		if result.Kind == command.ResultKindText {
			return data.Content
		}
	case *command.TextData:
		if data != nil && result.Kind == command.ResultKindText {
			return data.Content
		}
	case command.SessionData:
		if result.Kind == command.ResultKindCompact {
			return "compacted → " + data.ID
		}
	case *command.SessionData:
		if data != nil && result.Kind == command.ResultKindCompact {
			return "compacted → " + data.ID
		}
	case command.ModelSelectionData:
		prefix := "model"
		if data.Target != "current" {
			prefix = data.Target + ".model"
		}
		return fmt.Sprintf("%s → %s/%s", prefix, data.Model.Provider, data.Model.Model)
	case command.StopData:
		if data.State == "stopped" {
			return "stopped"
		}
		if !data.ShowCount {
			return "stopping"
		}
		return fmt.Sprintf("stopping: %d", data.AcceptedCount)
	case command.RollbackData:
		if data.Deleted {
			return "deleted: " + data.Path
		}
		return fmt.Sprintf("restored: %s · rev=%s", data.Path, data.Revision)
	case command.SessionChangeData:
		return formatSimpleSessionChange(data)
	case *command.SessionChangeData:
		if data != nil {
			return formatSimpleSessionChange(*data)
		}
	}
	return ""
}

func formatSimpleSessionChange(data command.SessionChangeData) string {
	if data.Prompt != "" || len(data.Messages) > 0 {
		return ""
	}
	switch data.Operation {
	case "new":
		return "New Session"
	case "work", "chat":
		if data.Session == nil {
			return ""
		}
		if data.Unchanged {
			return fmt.Sprintf("%s · %s · unchanged", data.Operation, data.Session.ID)
		}
		return fmt.Sprintf("%s · %s", data.Session.Mode, data.Session.ID)
	case "archive", "unarchive", "pin", "unpin", "rename":
		if data.Session == nil {
			return ""
		}
		verb := map[string]string{
			"archive": "archived", "unarchive": "unarchived", "pin": "pinned",
			"unpin": "unpinned", "rename": "renamed",
		}[data.Operation]
		return fmt.Sprintf("%s: %s · %s", verb, data.Session.Title, data.Session.ID)
	case "delete":
		return "deleted: " + data.SessionID
	case "clean":
		return fmt.Sprintf("cleaned: %d", data.AffectedCount)
	}
	return ""
}
