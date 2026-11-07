package share

import (
	"fmt"
	"strings"

	command "elbot/internal/command/share"
)

func formatSessionChange(d command.SessionChangeData) string {
	if d.Prompt != "" {
		return d.Prompt
	}
	switch d.Operation {
	case "new":
		return "New Session"
	case "resume", "fork":
		if d.Session == nil {
			return ""
		}
		var out strings.Builder
		if d.Operation == "resume" {
			fmt.Fprintf(&out, "resume → %s · %s\n", d.Session.Title, d.Session.ID)
		} else {
			fmt.Fprintf(&out, "fork → %s · %s\nparent=%s · from=%s\n", d.Session.ID, d.Session.Mode, d.Session.ParentSessionID, d.Session.ForkFromMessageID)
		}
		for _, message := range d.Messages {
			role := "user"
			if string(message.Role) == "assistant" {
				role = "bot"
			}
			fmt.Fprintf(&out, "%s: %s\n", role, preview(message.Content, 80))
		}
		return strings.TrimRight(out.String(), "\n")
	case "work", "chat":
		if d.Session == nil {
			return ""
		}
		if d.Unchanged {
			return fmt.Sprintf("%s · %s · unchanged", d.Operation, d.Session.ID)
		}
		return fmt.Sprintf("%s · %s", d.Session.Mode, d.Session.ID)
	case "archive", "unarchive", "pin", "unpin", "rename":
		if d.Session == nil {
			return ""
		}
		verb := map[string]string{"archive": "archived", "unarchive": "unarchived", "pin": "pinned", "unpin": "unpinned", "rename": "renamed"}[d.Operation]
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

func formatSessions(d command.SessionsData) string {
	var out strings.Builder
	for i, item := range d.Items {
		if i > 0 {
			out.WriteByte('\n')
		}
		title := item.Title
		if title == "" {
			title = "(untitled)"
		}
		markers := []string{}
		if item.ID == d.CurrentSessionID {
			markers = append(markers, "current")
		}
		if item.PinnedAt != nil {
			markers = append(markers, "pinned")
		}
		if item.ArchivedAt != nil {
			markers = append(markers, "archived")
		}
		mark := ""
		if len(markers) > 0 {
			mark = " [" + strings.Join(markers, ", ") + "]"
		}
		fmt.Fprintf(&out, "[%d] %s%s\n%s · %s/%s · %s · msgs=%d\n", (d.Page.Page-1)*d.Page.PageSize+i+1, title, mark, item.ID, item.Platform, item.ChatID, item.UpdatedAt.Format("2006-01-02 15:04:05"), item.MessageCount)
		var previews []string
		for index, message := range item.Messages {
			if index == 4 {
				break
			}
			content := strings.TrimSpace(strings.ReplaceAll(message.Content, "\n", " "))
			if content == "" {
				continue
			}
			role := "user"
			if string(message.Role) == "assistant" {
				role = "bot"
			}
			previews = append(previews, role+": "+preview(content, 24))
		}
		if len(item.Messages) > 4 {
			previews = append(previews, "...")
		}
		if len(previews) > 0 {
			out.WriteString(strings.Join(previews, " / ") + "\n")
		}
	}
	if out.Len() > 0 {
		out.WriteString("\n")
	}
	fmt.Fprintf(&out, "page=%d", d.Page.Page)
	if d.PrevCommand != "" {
		fmt.Fprintf(&out, " · prev: %s", d.PrevCommand)
	}
	if d.NextCommand != "" {
		fmt.Fprintf(&out, " · next: %s", d.NextCommand)
	}
	return strings.TrimRight(out.String(), "\n")
}

func formatMessages(d command.MessagesData) string {
	if len(d.Items) == 0 {
		return "nil"
	}
	var out strings.Builder
	pages := 1
	if d.Page.TotalPages != nil {
		pages = *d.Page.TotalPages
	}
	fmt.Fprintf(&out, "page=%d/%d\n", d.Page.Page, pages)
	for _, message := range d.Items {
		if string(message.Role) == "assistant" {
			fmt.Fprintf(&out, "bot: [%s] %s\n", message.ID, preview(message.Content, 40))
		} else {
			fmt.Fprintf(&out, "user: %s\n", preview(message.Content, 40))
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

func preview(text string, n int) string {
	text = strings.TrimSpace(strings.Join(strings.Fields(text), " "))
	runes := []rune(text)
	if len(runes) > n {
		return string(runes[:n]) + "..."
	}
	return text
}

func previewOrNil(text string, n int) string {
	if text == "" {
		return "nil"
	}
	return preview(text, n)
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
