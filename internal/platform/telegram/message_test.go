package telegram

import "testing"

func TestStripBotMentionFromCommand(t *testing.T) {
	text, mentioned := stripBotMention("/help@ElBot hi", "ElBot")
	if !mentioned {
		t.Fatal("mentioned = false")
	}
	if text != "/help hi" {
		t.Fatalf("text = %q", text)
	}
}

func TestStripBotMentionFromText(t *testing.T) {
	text, mentioned := stripBotMention("hello @ElBot", "ElBot")
	if !mentioned {
		t.Fatal("mentioned = false")
	}
	if text != "hello" {
		t.Fatalf("text = %q", text)
	}
}

func TestStripBotMentionFromTextCaseInsensitive(t *testing.T) {
	text, mentioned := stripBotMention("hello @elbot", "ElBot")
	if !mentioned {
		t.Fatal("mentioned = false")
	}
	if text != "hello" {
		t.Fatalf("text = %q", text)
	}
}

func TestNormalizeDocument(t *testing.T) {
	msg := message{Document: &document{FileID: "file-id", FileName: "a.txt", MIMEType: "text/plain"}}
	normalized := normalizeMessage(msg)
	if normalized.Text != "[文件]" {
		t.Fatalf("text = %q", normalized.Text)
	}
	if len(normalized.Segments) != 1 || normalized.Segments[0].Name != "a.txt" || normalized.Segments[0].MIMEType != "text/plain" || normalized.Segments[0].PlatformFileID != "file-id" || normalized.Segments[0].URL != "" {
		t.Fatalf("segments = %#v", normalized.Segments)
	}
}
