package hook

import (
	"fmt"
	"regexp"
	"strings"

	"elbot/internal/llm"
)

type RegexMatch struct {
	Field  string            `json:"field"`
	Value  string            `json:"value"`
	Text   string            `json:"text"`
	Groups []string          `json:"groups"`
	Named  map[string]string `json:"named,omitempty"`
	Start  int               `json:"start"`
	End    int               `json:"end"`
}

type MatchContext struct {
	Regex []RegexMatch `json:"regex,omitempty"`
}

const (
	MatchAlways   = "always"
	MatchExists   = "exists"
	MatchContains = "contains"
	MatchFull     = "fullmatch"
	MatchPrefix   = "startswith"
	MatchSuffix   = "endswith"
	MatchRegex    = "regex"
)

// Match is an explicit AND-list of conditions required before a hook runs.
type Match struct {
	Conditions []Condition
}

type Condition struct {
	Field string `toml:"field"`
	Op    string `toml:"op"`
	Value string `toml:"value"`
}

// BlockPolicy skips every registration contributed by one plugin before its
// normal match conditions or handler are evaluated.
type BlockPolicy struct {
	platforms map[string]struct{}
	groups    map[string]struct{}
	users     map[string]struct{}
}

func NewBlockPolicy(blockedPlatforms, blockedGroups, blockedUsers []string) (BlockPolicy, error) {
	policy := BlockPolicy{
		platforms: make(map[string]struct{}, len(blockedPlatforms)),
		groups:    make(map[string]struct{}, len(blockedGroups)),
		users:     make(map[string]struct{}, len(blockedUsers)),
	}
	for _, value := range blockedPlatforms {
		value = strings.TrimSpace(value)
		if value == "" {
			return BlockPolicy{}, fmt.Errorf("blocked_platform contains an empty platform")
		}
		policy.platforms[value] = struct{}{}
	}
	for _, item := range []struct {
		name   string
		values []string
		target map[string]struct{}
	}{
		{name: "blocked_group", values: blockedGroups, target: policy.groups},
		{name: "blocked_id", values: blockedUsers, target: policy.users},
	} {
		for _, value := range item.values {
			platform, id, ok := splitBlockedID(value)
			if !ok {
				return BlockPolicy{}, fmt.Errorf("%s entry %q must use <platform>:<id>", item.name, value)
			}
			item.target[platform+":"+id] = struct{}{}
		}
	}
	return policy, nil
}

func (p BlockPolicy) Blocks(event Event) bool {
	platform := strings.TrimSpace(event.Platform.Name)
	if platform == "" {
		return false
	}
	if _, ok := p.platforms[platform]; ok {
		return true
	}
	if groupID, ok := eventGroupID(event.Platform.ScopeID); ok {
		if _, blocked := p.groups[platform+":"+groupID]; blocked {
			return true
		}
	}
	userID := strings.TrimSpace(event.Actor.UserID)
	if userID == "" {
		userID = strings.TrimPrefix(strings.TrimSpace(event.Actor.ID), platform+":")
	}
	_, blocked := p.users[platform+":"+userID]
	return userID != "" && blocked
}

func splitBlockedID(value string) (string, string, bool) {
	platform, id, ok := strings.Cut(strings.TrimSpace(value), ":")
	platform = strings.TrimSpace(platform)
	id = strings.TrimSpace(id)
	return platform, id, ok && platform != "" && id != ""
}

func eventGroupID(scopeID string) (string, bool) {
	kind, id, ok := strings.Cut(strings.TrimSpace(scopeID), ":")
	id = strings.TrimSpace(id)
	return id, ok && id != "" && (kind == "group" || kind == "supergroup")
}

func Always() Match {
	return Match{Conditions: []Condition{{Op: MatchAlways}}}
}

func Contains(field, value string) Match {
	return Match{Conditions: []Condition{{Field: field, Op: MatchContains, Value: value}}}
}

func FullMatch(field, value string) Match {
	return Match{Conditions: []Condition{{Field: field, Op: MatchFull, Value: value}}}
}

func StartsWith(field, value string) Match {
	return Match{Conditions: []Condition{{Field: field, Op: MatchPrefix, Value: value}}}
}

func EndsWith(field, value string) Match {
	return Match{Conditions: []Condition{{Field: field, Op: MatchSuffix, Value: value}}}
}

func Regex(field, value string) Match {
	return Match{Conditions: []Condition{{Field: field, Op: MatchRegex, Value: value}}}
}

func Exists(field string) Match {
	return Match{Conditions: []Condition{{Field: field, Op: MatchExists}}}
}

func (m Match) Validate() error {
	if len(m.Conditions) == 0 {
		return fmt.Errorf("hook match requires at least one condition")
	}
	for i, cond := range m.Conditions {
		if err := cond.validate(); err != nil {
			return fmt.Errorf("condition %d: %w", i+1, err)
		}
	}
	return nil
}

func (m Match) Matches(event Event) bool {
	matched := m.MatchEvent(event)
	return matched.OK
}

type MatchResult struct {
	OK      bool
	Context MatchContext
}

func (m Match) MatchEvent(event Event) MatchResult {
	var ctx MatchContext
	for _, cond := range m.Conditions {
		ok, capture := cond.match(event)
		if !ok {
			return MatchResult{}
		}
		if capture != nil {
			ctx.Regex = append(ctx.Regex, *capture)
		}
	}
	return MatchResult{OK: true, Context: ctx}
}

func (c Condition) validate() error {
	op := strings.TrimSpace(c.Op)
	if op == "" {
		return fmt.Errorf("op is required")
	}
	if !knownMatchOp(op) {
		return fmt.Errorf("unsupported op %q", op)
	}
	if op == MatchAlways {
		if strings.TrimSpace(c.Field) != "" || strings.TrimSpace(c.Value) != "" {
			return fmt.Errorf("always cannot set field or value")
		}
		return nil
	}
	if !KnownField(c.Field) {
		return fmt.Errorf("unsupported field %q", c.Field)
	}
	if needsMatchValue(op) && c.Value == "" {
		return fmt.Errorf("value is required for %s", op)
	}
	if op == MatchRegex {
		if _, err := regexp.Compile(c.Value); err != nil {
			return fmt.Errorf("invalid regex: %w", err)
		}
	}
	return nil
}

func (c Condition) matches(event Event) bool {
	ok, _ := c.match(event)
	return ok
}

func (c Condition) match(event Event) (bool, *RegexMatch) {
	fieldValue := FieldValue(event, c.Field)
	switch strings.TrimSpace(c.Op) {
	case MatchAlways:
		return true, nil
	case MatchExists:
		return fieldValue != "", nil
	case MatchContains:
		return strings.Contains(fieldValue, c.Value), nil
	case MatchFull:
		return fieldValue == c.Value, nil
	case MatchPrefix:
		return strings.HasPrefix(fieldValue, c.Value), nil
	case MatchSuffix:
		return strings.HasSuffix(fieldValue, c.Value), nil
	case MatchRegex:
		capture, ok := regexMatchContext(c.Field, c.Value, fieldValue)
		if !ok {
			return false, nil
		}
		return true, &capture
	default:
		return false, nil
	}
}

func regexMatchContext(field, pattern, value string) (RegexMatch, bool) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return RegexMatch{}, false
	}
	indexes := re.FindStringSubmatchIndex(value)
	if indexes == nil {
		return RegexMatch{}, false
	}
	names := re.SubexpNames()
	groups := make([]string, 0, len(indexes)/2)
	named := map[string]string{}
	for i := 0; i < len(indexes); i += 2 {
		start, end := indexes[i], indexes[i+1]
		group := ""
		if start >= 0 && end >= 0 {
			group = value[start:end]
		}
		groups = append(groups, group)
		nameIndex := i / 2
		if nameIndex < len(names) && strings.TrimSpace(names[nameIndex]) != "" {
			named[names[nameIndex]] = group
		}
	}
	if len(named) == 0 {
		named = nil
	}
	return RegexMatch{Field: field, Value: pattern, Text: groups[0], Groups: groups, Named: named, Start: indexes[0], End: indexes[1]}, true
}

func knownMatchOp(op string) bool {
	switch op {
	case MatchAlways, MatchExists, MatchContains, MatchFull, MatchPrefix, MatchSuffix, MatchRegex:
		return true
	default:
		return false
	}
}

func needsMatchValue(op string) bool {
	switch op {
	case MatchContains, MatchFull, MatchPrefix, MatchSuffix, MatchRegex:
		return true
	default:
		return false
	}
}

func KnownField(field string) bool {
	switch strings.TrimSpace(field) {
	case "platform.name", "platform.scope_id", "platform.user_id", "platform.conversation_id", "platform.message_id", "platform.reply_to_message_id",
		"message.id", "message.text", "message.display_text", "message.platform_text", "message.intent_text", "message.role",
		"message.reply.message_id", "message.reply.sender_id", "message.reply.text", "message.reply.display_text",
		"llm.text", "llm.source_text", "llm.latest_user_text", "llm.latest_user_display_text", "llm.provider", "llm.model",
		"tool.name", "tool.arguments", "tool.result", "tool.risk",
		"actor.id", "actor.user_id", "actor.role", "actor.group_role", "actor.display_name",
		"session.id", "session.mode", "session.title", "session.status",
		"request.id", "request.kind", "request.session_id", "request.phase",
		"error.message":
		return true
	default:
		return false
	}
}

func FieldValue(event Event, field string) string {
	switch strings.TrimSpace(field) {
	case "platform.name":
		return event.Platform.Name
	case "platform.scope_id":
		return event.Platform.ScopeID
	case "platform.user_id":
		return event.Platform.UserID
	case "platform.conversation_id":
		return event.Platform.ConversationID
	case "platform.message_id":
		return event.Platform.PlatformMessageID
	case "platform.reply_to_message_id":
		return event.Platform.ReplyToMessageID
	case "message.id":
		return event.Message.ID
	case "message.text":
		return llm.SegmentsTextOnly(event.Message.Segments)
	case "message.display_text":
		return llm.SegmentsContentText(event.Message.Segments)
	case "message.platform_text":
		return event.Message.PlatformText
	case "message.intent_text":
		return MessageIntentText(event)
	case "message.role":
		return event.Message.Role
	case "message.reply.message_id":
		if event.Message.Reply == nil {
			return ""
		}
		return event.Message.Reply.MessageID
	case "message.reply.sender_id":
		if event.Message.Reply == nil {
			return ""
		}
		return event.Message.Reply.SenderID
	case "message.reply.text":
		if event.Message.Reply == nil {
			return ""
		}
		return event.Message.Reply.Text
	case "message.reply.display_text":
		if event.Message.Reply == nil {
			return ""
		}
		return event.Message.Reply.DisplayText
	case "llm.text":
		return event.LLM.Text
	case "llm.source_text":
		return event.LLM.SourceText
	case "llm.latest_user_text":
		return llm.LatestUserSegmentTextOnly(event.LLM.Messages)
	case "llm.latest_user_display_text":
		return llm.LatestUserSegmentContentText(event.LLM.Messages)
	case "llm.provider":
		return event.LLM.Provider
	case "llm.model":
		return event.LLM.Model
	case "tool.name":
		return event.Tool.Name
	case "tool.arguments":
		return event.Tool.Arguments
	case "tool.result":
		return event.Tool.Result
	case "tool.risk":
		return event.Tool.Risk
	case "actor.id":
		return event.Actor.ID
	case "actor.user_id":
		return event.Actor.UserID
	case "actor.role":
		return event.Actor.Role
	case "actor.group_role":
		return event.Actor.GroupRole
	case "actor.display_name":
		return event.Actor.DisplayName
	case "actor.nickname":
		return event.Actor.Nickname
	case "actor.group_card":
		return event.Actor.GroupCard
	case "session.id":
		return event.Session.ID
	case "session.mode":
		return event.Session.Mode
	case "session.title":
		return event.Session.Title
	case "session.status":
		return event.Session.Status
	case "request.id":
		return event.Request.ID
	case "request.kind":
		return event.Request.Kind
	case "request.session_id":
		return event.Request.SessionID
	case "request.phase":
		return event.Request.Phase
	case "error.message":
		return EventErrorMessage(event)
	default:
		return ""
	}
}

func TemplateValues(event Event) map[string]string {
	values := map[string]string{}
	for _, field := range []string{
		"platform.name", "platform.scope_id", "platform.user_id", "platform.conversation_id", "platform.message_id", "platform.reply_to_message_id",
		"actor.id", "actor.user_id", "actor.role", "actor.group_role", "actor.display_name",
		"session.id", "session.mode", "session.title", "session.status",
		"request.id", "request.kind", "request.session_id", "request.phase",
		"message.id", "message.text", "message.display_text", "message.platform_text", "message.intent_text", "message.role",
		"message.reply.message_id", "message.reply.sender_id", "message.reply.text", "message.reply.display_text",
		"llm.text", "llm.source_text", "llm.latest_user_text", "llm.latest_user_display_text", "llm.provider", "llm.model",
		"tool.name", "tool.arguments", "tool.result", "tool.risk",
		"error.message",
	} {
		values["{{"+field+"}}"] = FieldValue(event, field)
	}
	return values
}
