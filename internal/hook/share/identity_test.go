package share

import "testing"

func TestIdentityTemplateFields(t *testing.T) {
	event := Event{Actor: ActorContext{Platform: "qqonebot", ID: "user:42", Name: "名片"}, Chat: ChatContext{Platform: "qqonebot", ChatType: "group", ID: "group:42", Name: "群名"}}
	for field, want := range map[string]string{"actor.platform": "qqonebot", "actor.id": "user:42", "actor.name": "名片", "chat.platform": "qqonebot", "chat.chat_type": "group", "chat.id": "group:42", "chat.name": "群名"} {
		if !KnownField(field) || FieldValue(event, field) != want {
			t.Fatalf("%s = %q, want %q", field, FieldValue(event, field), want)
		}
	}
}
