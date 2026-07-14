package hook

import (
	"fmt"
	"strings"
	"unicode"
)

// SplitCommand parses a command without invoking a shell.
func SplitCommand(command string) ([]string, error) {
	var args []string
	var b strings.Builder
	runes := []rune(command)
	var quote rune
	tokenStarted := false
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quote != 0 {
			if r == '\\' && i+1 < len(runes) && (runes[i+1] == quote || runes[i+1] == '\\') {
				b.WriteRune(runes[i+1])
				i++
				continue
			}
			if r == quote {
				quote = 0
				continue
			}
			b.WriteRune(r)
			continue
		}
		if unicode.IsSpace(r) {
			if tokenStarted {
				args = append(args, b.String())
				b.Reset()
				tokenStarted = false
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			tokenStarted = true
			continue
		}
		b.WriteRune(r)
		tokenStarted = true
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in command")
	}
	if tokenStarted {
		args = append(args, b.String())
	}
	return args, nil
}
