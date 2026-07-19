package llm

import (
	"regexp"
	"testing"
)

func TestSegmentsTextOnlyAndContentText(t *testing.T) {
	segments := []MessageSegment{
		{Type: SegmentText, Text: "看看"},
		{Type: SegmentImage, URL: "https://example.com/a.png", Name: "a.png"},
		{Type: SegmentFile, URL: "file:///tmp/report.pdf", Name: "report.pdf"},
	}
	if got := SegmentsTextOnly(segments); got != "看看" {
		t.Fatalf("SegmentsTextOnly = %q", got)
	}
	if got := SegmentsContentText(segments); got != "看看 [图片: https://example.com/a.png] [文件: file:///tmp/report.pdf]" {
		t.Fatalf("SegmentsContentText = %q", got)
	}
}

func TestSegmentsContentTextDoesNotInlineDataURL(t *testing.T) {
	segments := []MessageSegment{{Type: SegmentText, Text: "done"}, {Type: SegmentImage, URL: "data:image/png;base64,aGVsbG8=", Name: "result.png"}}
	if got := SegmentsContentText(segments); got != "done [图片: result.png]" {
		t.Fatalf("SegmentsContentText = %q", got)
	}
}

func TestSetSegmentTextKeepsMediaAndCollapsesTextSegments(t *testing.T) {
	segments := []MessageSegment{{Type: SegmentText, Text: "old"}, {Type: SegmentImage, URL: "image"}, {Type: SegmentText, Text: "tail"}}
	got := SetSegmentText(segments, "new")
	if len(got) != 2 || got[0].Text != "new" || got[1].Type != SegmentImage {
		t.Fatalf("segments = %#v", got)
	}
}

func TestPrependAppendSegmentTextKeepsMediaInPlace(t *testing.T) {
	segments := []MessageSegment{
		{Type: SegmentImage, URL: "image"},
		{Type: SegmentText, Text: "hello"},
		{Type: SegmentFile, Name: "file.txt"},
	}
	segments = PrependSegmentText(segments, "pre ")
	segments = AppendSegmentText(segments, " post")
	if segments[0].Type != SegmentImage || segments[2].Type != SegmentFile {
		t.Fatalf("media moved: %#v", segments)
	}
	if segments[1].Text != "pre hello post" {
		t.Fatalf("text segment = %q", segments[1].Text)
	}
}

func TestPrependAppendSegmentTextAddsTextWhenMissing(t *testing.T) {
	segments := []MessageSegment{{Type: SegmentImage, URL: "image"}}
	prepended := PrependSegmentText(segments, "pre")
	if len(prepended) != 2 || prepended[0].Type != SegmentText || prepended[0].Text != "pre" || prepended[1].Type != SegmentImage {
		t.Fatalf("prepended = %#v", prepended)
	}
	appended := AppendSegmentText(segments, "post")
	if len(appended) != 2 || appended[0].Type != SegmentImage || appended[1].Type != SegmentText || appended[1].Text != "post" {
		t.Fatalf("appended = %#v", appended)
	}
}

func TestReplaceSegmentTextFirstAndAllAcrossSegments(t *testing.T) {
	segments := []MessageSegment{
		{Type: SegmentText, Text: "cat one cat"},
		{Type: SegmentImage, URL: "image"},
		{Type: SegmentText, Text: "cat two"},
	}
	first := ReplaceSegmentText(segments, regexp.MustCompile("cat"), "dog", false)
	if first[0].Text != "dog one cat" || first[2].Text != "cat two" {
		t.Fatalf("first replace = %#v", first)
	}
	all := ReplaceSegmentText(segments, regexp.MustCompile("cat"), "dog", true)
	if all[0].Text != "dog one dog" || all[2].Text != "dog two" {
		t.Fatalf("all replace = %#v", all)
	}
}

func TestSegmentQueries(t *testing.T) {
	segments := []MessageSegment{
		{Type: SegmentText, Text: "text"},
		{Type: SegmentImage, URL: "image"},
		{Type: SegmentFile, Name: "file"},
	}
	if !HasImageSegment(segments) {
		t.Fatal("HasImageSegment = false")
	}
	if len(ImageSegments(segments)) != 1 || len(FileSegments(segments)) != 1 {
		t.Fatalf("images/files = %#v / %#v", ImageSegments(segments), FileSegments(segments))
	}
}
