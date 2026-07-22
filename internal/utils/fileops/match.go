package fileops

import (
	"fmt"
	"strings"
)

const (
	matchPreviewLimit = 5
	matchPreviewWidth = 40
)

type matchLocation struct {
	byteStart int
	byteEnd   int
	startLine int
	endLine   int
	preview   string
	lineIndex int
}

func findContentMatches(text, needle string) []matchLocation {
	needleNorm := NormalizeEditText(needle)
	if needleNorm == "" {
		return nil
	}
	var locations []matchLocation
	offset := 0
	for {
		idx := strings.Index(text[offset:], needleNorm)
		if idx < 0 {
			break
		}
		start := offset + idx
		end := start + len(needleNorm)
		locations = append(locations, matchLocation{
			byteStart: start,
			byteEnd:   end,
			startLine: byteOffsetToLine(text, start),
			endLine:   byteOffsetToLine(text, end-1),
			preview:   matchPreview(text, start, end),
		})
		offset = end
	}
	return locations
}

func findLineMatches(lines []editSourceLine, needle string) []matchLocation {
	var locations []matchLocation
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line.text, " \t"), needle) {
			locations = append(locations, matchLocation{
				byteStart: line.start,
				byteEnd:   line.end,
				startLine: i + 1,
				endLine:   i + 1,
				preview:   truncateLine(strings.TrimLeft(line.text, " \t")),
				lineIndex: i,
			})
		}
	}
	return locations
}

func selectMatches(locations []matchLocation, index *int, all bool, label string) ([]matchLocation, error) {
	n := len(locations)
	if n == 0 {
		return nil, fmt.Errorf("%s not found", label)
	}
	if index != nil && all {
		return nil, fmt.Errorf("index and all_matches are mutually exclusive")
	}
	if all {
		return locations, nil
	}
	if index != nil {
		i := *index
		if i < 1 || i > n {
			return nil, fmt.Errorf("index %d out of range: %d %s found, use 1-%d", i, n, label, n)
		}
		return []matchLocation{locations[i-1]}, nil
	}
	if n > 1 {
		return nil, fmt.Errorf("%s matched %d locations; provide a longer %s, index, or all_matches:\n%s", label, n, label, formatMatchLocations(locations))
	}
	return locations, nil
}

func formatMatchLocations(locations []matchLocation) string {
	var b strings.Builder
	limit := len(locations)
	if limit > matchPreviewLimit {
		limit = matchPreviewLimit
	}
	for i := 0; i < limit; i++ {
		loc := locations[i]
		fmt.Fprintf(&b, "  #%d %s %q\n", i+1, formatLineRange(loc), loc.preview)
	}
	if len(locations) > limit {
		fmt.Fprintf(&b, "  ...and %d more\n", len(locations)-limit)
	}
	return b.String()
}

func formatLineRange(loc matchLocation) string {
	if loc.startLine == loc.endLine {
		return fmt.Sprintf("L%d", loc.startLine)
	}
	return fmt.Sprintf("L%d-%d", loc.startLine, loc.endLine)
}

func byteOffsetToLine(text string, offset int) int {
	if offset < 0 {
		offset = 0
	}
	if offset > len(text) {
		offset = len(text)
	}
	return strings.Count(text[:offset], "\n") + 1
}

func matchPreview(text string, start, end int) string {
	lineStart := start
	for lineStart > 0 && text[lineStart-1] != '\n' {
		lineStart--
	}
	return truncateLine(text[lineStart:end])
}

func truncateLine(s string) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > matchPreviewWidth {
		return s[:matchPreviewWidth] + "..."
	}
	return s
}
