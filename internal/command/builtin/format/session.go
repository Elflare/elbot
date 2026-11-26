package format

import (
	"fmt"
	"strings"

	"elbot/internal/control"
	"elbot/internal/delivery"
)

func SessionChange(operation string, d control.SessionChangeResult, opts MessagesOptions) string {
	switch operation {
	case "new":
		return "New Session"
	case "resume", "fork":
		if d.Session == nil {
			return ""
		}
		var out strings.Builder
		if operation == "resume" {
			fmt.Fprintf(&out, "resume → %s · %s\n", d.Session.Title, d.Session.ID)
		} else {
			fmt.Fprintf(&out, "fork → %s · %s\nparent=%s · from=%s\n", d.Session.ID, d.Session.Mode, d.Session.ParentSessionID, d.Session.ForkFromMessageID)
		}
		for _, message := range d.Messages {
			role := "user"
			if string(message.Role) == "assistant" {
				role = "bot"
			}
			fmt.Fprintf(&out, "%s: %s\n", role, delivery.MessagePreview(message, opts.PreviewLimit))
		}
		return strings.TrimRight(out.String(), "\n")
	case "work", "chat":
		if d.Session == nil {
			return ""
		}
		if d.Unchanged {
			return fmt.Sprintf("%s · %s · unchanged", operation, d.Session.ID)
		}
		return fmt.Sprintf("%s · %s", d.Session.Mode, d.Session.ID)
	case "archive", "unarchive", "pin", "unpin", "rename":
		if d.Session == nil {
			return ""
		}
		verb := map[string]string{"archive": "archived", "unarchive": "unarchived", "pin": "pinned", "unpin": "unpinned", "rename": "renamed"}[operation]
		return fmt.Sprintf("%s: %s · %s", verb, d.Session.Title, d.Session.ID)
	case "delete":
		return "deleted: " + d.SessionID
	case "clean":
		return fmt.Sprintf("cleaned: %d", d.AffectedCount)
	case "confirm":
		return ""
	}
	return ""
}

func Messages(d control.SessionMessagesResult, opts MessagesOptions) string {
	if len(d.Items) == 0 {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "page=%d/%d\n", d.Page.Page, d.TotalPages)
	for _, message := range d.Items {
		if string(message.Role) == "assistant" {
			fmt.Fprintf(&out, "bot: [%s] %s\n", message.ID, delivery.MessagePreview(message, opts.PreviewLimit))
		} else {
			fmt.Fprintf(&out, "user: %s\n", delivery.MessagePreview(message, 40))
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

func statusPreviewOrNil(text string, n int) string {
	if text == "" {
		return "nil"
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	runes := []rune(text)
	if len(runes) > n {
		return string(runes[:n]) + "..."
	}
	return text
}
