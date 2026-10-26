package share

import (
	"elbot/internal/delivery"
	"reflect"
	"testing"
)

func TestMapReportReceiptUsesOnlyActualSentMessages(t *testing.T) {
	receipt := delivery.Receipt{PlatformMessageIDs: []string{"unattributed"}, SentMessages: []delivery.SentMessage{
		{Platform: "telegram", ScopeID: "private:1", PlatformMessageID: "77"},
		{Platform: "telegram", ScopeID: "private:2", PlatformMessageID: "77"},
		{Platform: "qqonebot", ScopeID: "private:1", PlatformMessageID: "77"},
	}}
	want := []ReportMessageMap{
		{Platform: "telegram", PlatformScopeID: "private:1", PlatformMessageID: "77", SessionID: "session", MessageID: "message"},
		{Platform: "telegram", PlatformScopeID: "private:2", PlatformMessageID: "77", SessionID: "session", MessageID: "message"},
		{Platform: "qqonebot", PlatformScopeID: "private:1", PlatformMessageID: "77", SessionID: "session", MessageID: "message"},
	}
	if got := MapReportReceipt("session", "message", receipt); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	for _, ids := range [][2]string{{"", "message"}, {"session", ""}, {"", ""}} {
		if got := MapReportReceipt(ids[0], ids[1], receipt); got != nil {
			t.Fatal(got)
		}
	}
	if got := MapReportReceipt("session", "message", delivery.Receipt{PlatformMessageIDs: []string{"77"}}); len(got) != 0 {
		t.Fatal(got)
	}
}
