package chat_history

import (
	"fmt"
	"strings"
)

const (
	chatHistoryMessageLimit = 300
)

// Keep participant IDs in a single roster, for history rows.
func formatHistoryRows(messages []Message, target int64, full, avatars bool) []string {
	type person struct{ id, name, url string }
	var people []person
	users := make(map[string]int)
	user := func(id, name, url string) string {
		if id == "" {
			return name
		}
		i, ok := users[id]
		if !ok {
			i = len(people)
			users[id] = i
			people = append(people, person{id: id})
		}
		p := &people[i]
		if name != "" {
			p.name = name
		}
		if url != "" {
			p.url = url
		}
		return fmt.Sprintf("u%d", i+1)
	}
	var lines []string
	day := ""
	for _, m := range messages {
		d := m.CreatedAt.Format("2006-01-02")
		if d != day {
			lines = append(lines, d)
			day = d
		}
		who := user(m.SenderID, m.SenderName, m.SenderAvatarURL)
		if m.IsAssistant {
			who = strings.TrimSpace("助手 " + who)
		}
		body := m.Text
		marker := "...[单条消息过长，已截断]"
		if m.Quote != "" {
			body = truncateChatHistoryReference(m.Quote, full, marker) + "\n" + truncateChatHistoryReference(body, full, marker)
		} else {
			body = truncateChatHistoryMessage(body, full, marker)
		}
		id := fmt.Sprintf("h%d", m.Seq)
		prefix := ""
		if target != 0 && target == m.Seq {
			prefix = "=> "
		}
		lines = append(lines, fmt.Sprintf("%s%s %s %s: %s", prefix, id, m.CreatedAt.Format("15:04:05"), who, body))
		for i, item := range m.Media {
			lines = append(lines, fmt.Sprintf("  %d. %s", i+1, historyMediaLabel(item.IsImage, item.MediaID)))
		}

	}
	if len(people) > 0 {
		lines = append(lines, "参与者")
	}
	for i, p := range people {
		parts := []string{fmt.Sprintf("u%d", i+1)}
		if p.name != "" && p.name != p.id {
			parts = append(parts, p.name)
		}
		parts = append(parts, p.id)
		if avatars && p.url != "" {
			parts = append(parts, p.url)
		}
		lines = append(lines, strings.Join(parts, " "))
	}
	return lines
}

func truncateChatHistoryReference(text string, full bool, marker string) string {
	if full {
		return text
	}
	return truncatePreview(strings.TrimSpace(strings.ReplaceAll(text, "\r", "")), marker)
}
func truncateChatHistoryMessage(text string, full bool, marker string) string {
	if full {
		return text
	}
	return truncatePreview(strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(text)), marker)
}
func truncatePreview(text, marker string) string {
	n := 0
	for i := range text {
		if n == chatHistoryMessageLimit {
			return text[:i] + marker
		}
		n++
	}
	return text
}
