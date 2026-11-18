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
	byteStart  int
	byteEnd    int
	anchorLine int // 1-based for line anchors; zero for content matches.
}

func findContentMatches(text, needle string) []matchLocation {
	var locations []matchLocation
	offset := 0
	for {
		idx := strings.Index(text[offset:], needle)
		if idx < 0 {
			break
		}
		start := offset + idx
		end := start + len(needle)
		locations = append(locations, matchLocation{
			byteStart: start,
			byteEnd:   end,
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
				byteStart:  line.start,
				byteEnd:    line.end,
				anchorLine: i + 1,
			})
		}
	}
	return locations
}

func selectMatches(text string, locations []matchLocation, index *int, all bool, label string) ([]matchLocation, error) {
	n := len(locations)
	if n == 0 {
		return nil, fmt.Errorf("%s not found", label)
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
		return nil, fmt.Errorf("%s matched %d locations; provide a longer %s, index, or all_matches:\n%s", label, n, label, formatMatchLocations(text, locations))
	}
	return locations, nil
}

func formatMatchLocations(text string, locations []matchLocation) string {
	var b strings.Builder
	limit := len(locations)
	if limit > matchPreviewLimit {
		limit = matchPreviewLimit
	}
	for i := 0; i < limit; i++ {
		loc := locations[i]
		if loc.anchorLine != 0 {
			line := strings.TrimLeft(text[loc.byteStart:loc.byteEnd], " \t")
			fmt.Fprintf(&b, "  #%d L%d %q\n", i+1, loc.anchorLine, truncateLine(line))
			continue
		}
		start := byteOffsetToLine(text, loc.byteStart)
		end := byteOffsetToLine(text, loc.byteEnd-1)
		fmt.Fprintf(&b, "  #%d %s %q\n", i+1, formatLineRange(start, end), matchPreview(text, loc.byteStart, loc.byteEnd))
	}
	if len(locations) > limit {
		fmt.Fprintf(&b, "  ...and %d more\n", len(locations)-limit)
	}
	return b.String()
}

func formatLineRange(start, end int) string {
	if start == end {
		return fmt.Sprintf("L%d", start)
	}
	return fmt.Sprintf("L%d-%d", start, end)
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
