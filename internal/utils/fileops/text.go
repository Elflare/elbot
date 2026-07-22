package fileops

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	DefaultReadLineLimit = 200
	contentRevisionBytes = 8
)

type LineNumber struct {
	Value int
	End   bool
}

func (n *LineNumber) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "" || text == "null" {
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		str = strings.ToLower(strings.TrimSpace(str))
		if str == "" {
			return nil
		}
		if str == "end" {
			n.End = true
			n.Value = 0
			return nil
		}
		value, err := strconv.Atoi(str)
		if err != nil {
			return fmt.Errorf("line number string must be integer or \"end\"")
		}
		n.Value = value
		n.End = false
		return nil
	}
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("line number must be integer or \"end\"")
	}
	n.Value = value
	n.End = false
	return nil
}

func NormalizeReadRange(total, start int, endLine LineNumber) (int, int, bool, error) {
	if total == 0 {
		return 1, 0, false, nil
	}
	if start <= 0 {
		start = 1
	}
	if start > total {
		return 0, 0, false, fmt.Errorf("start_line %d exceeds total lines %d", start, total)
	}
	truncated := false
	end := endLine.Value
	if endLine.End {
		end = total
	} else if end <= 0 {
		end = start + DefaultReadLineLimit - 1
		truncated = end < total
	}
	if end > total {
		end = total
	}
	if end < start {
		return 0, 0, false, fmt.Errorf("end_line must be >= start_line")
	}
	if !endLine.End && end-start+1 > DefaultReadLineLimit {
		end = start + DefaultReadLineLimit - 1
		truncated = true
	}
	return start, end, truncated, nil
}

func EnsureTrailingNewline(text string) string {
	text = NormalizeEditText(text)
	if text == "" || strings.HasSuffix(text, "\n") {
		return text
	}
	return text + "\n"
}

func NormalizeEditText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

func RestoreLineEndings(text, lineEnding string) string {
	if lineEnding == "" || lineEnding == "\n" {
		return text
	}
	return strings.ReplaceAll(text, "\n", lineEnding)
}

func NormalizeGrepContextLines(value int) int {
	if value < 0 {
		return 0
	}
	if value == 0 {
		return 2
	}
	if value > 20 {
		return 20
	}
	return value
}

func NormalizeMaxMatches(value int) int {
	if value <= 0 {
		return 20
	}
	if value > 100 {
		return 100
	}
	return value
}

func NormalizeContextLines(value int) int {
	if value < 0 {
		return 0
	}
	if value == 0 {
		return 3
	}
	if value > 20 {
		return 20
	}
	return value
}

func ValidateLineRange(total, start, end int) error {
	if start <= 0 {
		return fmt.Errorf("line must be >= 1")
	}
	if end < start {
		return fmt.Errorf("end_line must be >= line")
	}
	if total == 0 {
		return fmt.Errorf("file has no lines")
	}
	if start > total || end > total {
		return fmt.Errorf("line range %d-%d exceeds total lines %d", start, end, total)
	}
	return nil
}

func SplitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text == "" {
		return nil
	}
	if strings.HasSuffix(text, "\n") {
		text = strings.TrimSuffix(text, "\n")
	}
	if text == "" {
		return []string{""}
	}
	return strings.Split(text, "\n")
}

func DetectLineEnding(text string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	if strings.Contains(text, "\r") {
		return "\r"
	}
	return "\n"
}

func LooksBinary(data []byte) bool {
	limit := len(data)
	if limit > 4096 {
		limit = 4096
	}
	return bytes.Contains(data[:limit], []byte{0})
}

func ContentRevision(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:contentRevisionBytes])
}
