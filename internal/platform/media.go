package platform

import (
	"encoding/json"
	"net/url"
	"path"
	"strings"
)

// MarshalChatSegments stores ordered platform facts without materialized or local sources.
func MarshalChatSegments(segments []MessageSegment) string {
	stored := make([]MessageSegment, 0, len(segments))
	for _, segment := range segments {
		if segment.Type != SegmentText && segment.Type != SegmentImage && segment.Type != SegmentFile {
			continue
		}
		segment.MediaID = ""
		segment.URL = chatMediaURL(segment.URL)
		if segment.Type != SegmentText {
			if segment.PlatformFileID != "" && (strings.ContainsAny(segment.PlatformFileID, "/\\\\") || strings.Contains(segment.PlatformFileID, ":")) {
				segment.PlatformFileID = ""
			}
			if strings.Contains(segment.Name, "://") || strings.HasPrefix(segment.Name, "data:") || strings.HasPrefix(segment.Name, "base64:") {
				segment.Name = ""
			} else {
				segment.Name = path.Base(strings.ReplaceAll(segment.Name, "\\", "/"))
				if segment.Name == "." {
					segment.Name = ""
				}
			}
		}
		stored = append(stored, segment)
	}
	if len(stored) == 0 {
		return ""
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return ""
	}
	return string(data)
}

func UnmarshalChatSegments(value string) []MessageSegment {
	var segments []MessageSegment
	if json.Unmarshal([]byte(strings.TrimSpace(value)), &segments) != nil {
		return nil
	}
	return segments
}

func chatMediaURL(value string) string {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	// Telegram download paths contain the bot token, even without a query string.
	if strings.Contains(strings.ToLower(u.Path), "/bot") {
		return ""
	}
	for key := range u.Query() {
		key = strings.ToLower(key)
		if strings.HasPrefix(key, "x-amz-") || strings.HasPrefix(key, "x-goog-") || strings.Contains(key, "token") || strings.Contains(key, "signature") || key == "sig" || key == "rkey" || key == "auth_key" {
			return ""
		}
	}
	return u.String()
}
