package share

import (
	"fmt"
	"sort"
	"strings"
	"time"

	command "elbot/internal/command/share"
)

func formatStatus(d command.StatusData) string {
	if d.Session == nil {
		var out strings.Builder
		fmt.Fprintf(&out, "session=nil\ndefault=%s · model=%s/%s\nphase=%s · pending=%d", d.Mode, d.Model.Provider, d.Model.Model, d.Phase, d.PendingCount)
		fmt.Fprintf(&out, "\ncompact.model=%s/%s\nplatform=%s/%s", d.CompactModel.Provider, d.CompactModel.Model, d.Platform, d.ChatID)
		appendStatusErrors(&out, d.Errors)
		return strings.TrimRight(out.String(), "\n")
	}
	s := d.Session
	var out strings.Builder
	fmt.Fprintf(&out, "%s · %s\n%s · %s/%s · msgs=%d\n", s.ID, s.Title, d.Mode, d.Model.Provider, d.Model.Model, d.MessageCount)
	if s.ArchivedAt != nil || s.PinnedAt != nil {
		var marks []string
		if s.PinnedAt != nil {
			marks = append(marks, "pinned")
		}
		if s.ArchivedAt != nil {
			marks = append(marks, "archived")
		}
		fmt.Fprintf(&out, "  %s\n", strings.Join(marks, ", "))
	}
	fmt.Fprintf(&out, "created=%s\nupdated=%s\nuser: %s\nbot: %s\n", s.CreatedAt.Format("2006-01-02 15:04:05"), s.UpdatedAt.Format("2006-01-02 15:04:05"), statusPreviewOrNil(d.LastUserContent, 80), statusPreviewOrNil(d.LastAssistantContent, 80))
	if requests := formatActiveRequestsData(d.Requests); requests != "" {
		out.WriteString(requests)
	}
	fmt.Fprintf(&out, "phase=%s · pending=%d\n", d.Phase, d.PendingCount)
	if tools := formatToolUsageData(d.ToolUsage, d.TurnToolUsage); tools != "" {
		out.WriteString(tools)
	}
	appendStatusContext(&out, d.Context)
	appendStatusErrors(&out, d.Errors)
	return strings.TrimRight(out.String(), "\n")
}

func formatActiveRequestsData(requests []command.RequestData) string {
	if len(requests) == 0 {
		return ""
	}
	children := make(map[string][]command.RequestData)
	byID := make(map[string]command.RequestData, len(requests))
	for _, item := range requests {
		byID[item.ID] = item
	}
	var roots []command.RequestData
	for _, item := range requests {
		if item.ParentID != "" {
			if _, ok := byID[item.ParentID]; ok {
				children[item.ParentID] = append(children[item.ParentID], item)
				continue
			}
		}
		roots = append(roots, item)
	}
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].StartedAt.Before(roots[j].StartedAt) })
	var out strings.Builder
	out.WriteString("requests:\n")
	for i, item := range roots {
		writeRequestDataLine(&out, item, fmt.Sprint(i+1), "")
		sort.SliceStable(children[item.ID], func(i, j int) bool { return children[item.ID][i].StartedAt.Before(children[item.ID][j].StartedAt) })
		for j, child := range children[item.ID] {
			writeRequestDataLine(&out, child, fmt.Sprintf("%d.%d", i+1, j+1), "└── ")
		}
	}
	return out.String()
}

func writeRequestDataLine(out *strings.Builder, item command.RequestData, number, prefix string) {
	name := item.Label
	if name == "" {
		if item.Kind == "turn" {
			name = "chat"
		} else {
			name = "request"
		}
	}
	if item.Stopping {
		name += " stopping"
	}
	duration := "0s"
	if elapsed := time.Since(item.StartedAt); elapsed >= time.Second {
		duration = elapsed.Round(time.Second).String()
	}
	fmt.Fprintf(out, "  %s[%s] %s: %s · %s\n", prefix, number, item.Kind, name, duration)
}

func formatToolUsageData(saved, live map[string]int) string {
	var out strings.Builder
	append := func(label string, usage map[string]int) {
		if len(usage) == 0 {
			return
		}
		keys := make([]string, 0, len(usage))
		for key := range usage {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, fmt.Sprintf("%s×%d", key, usage[key]))
		}
		fmt.Fprintf(&out, "tools.%s: %s\n", label, strings.Join(parts, ", "))
	}
	append("saved", saved)
	append("turn", live)
	return out.String()
}

func appendStatusContext(out *strings.Builder, data command.ContextData) {
	usage := data.Usage
	tokens, cache := "unk", "unk"
	if usage != nil {
		if usage.TotalTokens != nil && *usage.TotalTokens >= 0 {
			tokens = fmt.Sprint(*usage.TotalTokens)
		}
		if usage.CacheHitTokens != nil && *usage.CacheHitTokens >= 0 {
			cache = fmt.Sprint(*usage.CacheHitTokens)
			if usage.PromptTokens != nil && *usage.PromptTokens > 0 {
				cache += fmt.Sprintf(" (%.1f%%)", float64(*usage.CacheHitTokens)/float64(*usage.PromptTokens)*100)
			}
		}
	}
	fmt.Fprintf(out, "tokens=%s · cache=%s\n", tokens, cache)
	if usage == nil {
		fmt.Fprintf(out, "ctx.window=%d · ratio=unk\n", data.ContextWindow)
	} else if usage.TotalTokens == nil || *usage.TotalTokens < 0 || data.ContextWindow <= 0 {
		fmt.Fprintf(out, "ctx.window=%d · ratio=unk\n", data.ContextWindow)
	} else {
		ratio := float64(*usage.TotalTokens) / float64(data.ContextWindow)
		fmt.Fprintf(out, "ctx=%d/%d (%.1f%%)\n", *usage.TotalTokens, data.ContextWindow, ratio*100)
	}
	state := "unk"
	if !data.CompactEnabled {
		state = "off"
	} else if usage != nil && usage.TotalTokens != nil && *usage.TotalTokens >= 0 && data.ContextWindow > 0 {
		ratio := float64(*usage.TotalTokens) / float64(data.ContextWindow)
		state = "ok"
		if ratio >= data.CompactTriggerRatio {
			state = "next request"
		} else if ratio >= max(0, data.CompactTriggerRatio-0.1) {
			state = "near"
		}
	}
	fmt.Fprintf(out, "compact=%.0f%% · %s\n", data.CompactTriggerRatio*100, state)
}

func appendStatusErrors(out *strings.Builder, errors []command.ItemError) {
	for _, failure := range errors {
		if failure.Error != nil {
			fmt.Fprintf(out, "warn: %s: %s\n", failure.ID, failure.Error.Message)
		}
	}
}
