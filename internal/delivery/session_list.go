package delivery

import (
	controlshare "elbot/internal/control/share"
	"elbot/internal/llm"
	"fmt"
	"strings"
)

func SessionListText(d controlshare.ListSessionsResult) string {
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
		fmt.Fprintf(&out, "[%s] %s%s\n%s/%s · %s · msgs=%d\n", item.ID, title, mark, item.Platform, item.ChatID, item.UpdatedAt.Format("2006-01-02 15:04:05"), item.MessageCount)
		var previews []string
		if len(item.Messages) > 4 {
			previews = append(previews, "...")
		}
		for _, message := range item.Messages[max(0, len(item.Messages)-4):] {
			content := MessagePreview(message, 24)
			if content == "" {
				continue
			}
			role := "user"
			if string(message.Role) == "assistant" {
				role = "bot"
			}
			previews = append(previews, role+": "+content)
		}
		if len(previews) > 0 {
			out.WriteString(strings.Join(previews, " / ") + "\n")
		}
	}
	if out.Len() > 0 {
		out.WriteString("\n")
	}
	fmt.Fprintf(&out, "page=%d/%d", d.Page.Page, d.TotalPages)
	return out.String()
}

func MessagePreview(message controlshare.MessageData, n int) string {
	parts := []string{}
	if text := Preview(message.Content, n); text != "" {
		parts = append(parts, text)
	}
	for _, attachment := range message.Segments {
		if attachment.Type != llm.SegmentFile && attachment.Type != llm.SegmentImage {
			continue
		}
		if attachment.Name == "" {
			parts = append(parts, "[附件]")
		} else {
			parts = append(parts, "[附件: "+attachment.Name+"]")
		}
	}
	return strings.Join(parts, " ")
}

func Preview(text string, n int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if n > 0 && len(runes) > n {
		return string(runes[:n]) + "..."
	}
	return text
}
