// 定位并展示 Hook 配置的 TOML 错误。

package rules

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

func decodeTOMLFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read hook rule config %q: %w", path, err)
	}
	decoder := toml.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return detailedTOMLDecodeError(path, err)
	}
	return nil
}

func detailedTOMLDecodeError(path string, err error) error {
	var strictErr *toml.StrictMissingError
	if errors.As(err, &strictErr) && len(strictErr.Errors) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "parse hook rule config %q", path)
		for _, decodeErr := range strictErr.Errors {
			appendDecodeErrorDetail(&b, decodeErr)
		}
		return configParseError{message: b.String(), cause: err}
	}

	var decodeErr *toml.DecodeError
	if errors.As(err, &decodeErr) {
		var b strings.Builder
		fmt.Fprintf(&b, "parse hook rule config %q", path)
		appendDecodeErrorDetail(&b, *decodeErr)
		return configParseError{message: b.String(), cause: err}
	}

	return fmt.Errorf("parse hook rule config %q: %w", path, err)
}

func appendDecodeErrorDetail(b *strings.Builder, err toml.DecodeError) {
	row, column := err.Position()
	key := strings.Join([]string(err.Key()), ".")
	fmt.Fprintf(b, "\n- line %d, column %d", row, column)
	if key != "" {
		fmt.Fprintf(b, ", field %q", key)
	}
	fmt.Fprintf(b, ": %s", err.Error())
	if snippet := strings.TrimRight(err.String(), "\n"); snippet != "" {
		b.WriteString("\n")
		b.WriteString(indentLines(snippet, "  "))
	}
}

func indentLines(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i := range lines {
		if lines[i] != "" {
			lines[i] = prefix + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

type configParseError struct {
	message string
	cause   error
}

func (e configParseError) Error() string { return e.message }

func (e configParseError) Unwrap() error { return e.cause }
