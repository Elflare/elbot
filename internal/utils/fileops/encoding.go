package fileops

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
)

func DecodeBytes(data []byte, requested string) (string, string, []byte, error) {
	name := strings.ToLower(strings.TrimSpace(requested))
	if name == "" || name == "auto" {
		switch {
		case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
			return string(data[3:]), "utf-8-bom", []byte{0xEF, 0xBB, 0xBF}, nil
		case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
			return decodeWithEncoding(data[2:], "utf-16le", []byte{0xFF, 0xFE})
		case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
			return decodeWithEncoding(data[2:], "utf-16be", []byte{0xFE, 0xFF})
		default:
			if !utf8.Valid(data) {
				return "", "", nil, fmt.Errorf("file is not valid UTF-8; pass encoding explicitly")
			}
			return string(data), "utf-8", nil, nil
		}
	}
	if name == "utf8" {
		name = "utf-8"
	}
	if name == "utf-8-bom" {
		if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
			data = data[3:]
		}
		if !utf8.Valid(data) {
			return "", "", nil, fmt.Errorf("file is not valid UTF-8")
		}
		return string(data), "utf-8-bom", []byte{0xEF, 0xBB, 0xBF}, nil
	}
	if name == "utf-8" {
		if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
			data = data[3:]
		}
		if !utf8.Valid(data) {
			return "", "", nil, fmt.Errorf("file is not valid UTF-8")
		}
		return string(data), "utf-8", nil, nil
	}
	return decodeWithEncoding(data, name, nil)
}

func decodeWithEncoding(data []byte, name string, bom []byte) (string, string, []byte, error) {
	enc, err := LookupEncoding(name)
	if err != nil {
		return "", "", nil, err
	}
	decoded, _, err := transform.Bytes(enc.NewDecoder(), data)
	if err != nil {
		return "", "", nil, fmt.Errorf("decode %s: %w", name, err)
	}
	return string(decoded), name, bom, nil
}

func EncodeText(text, name string, bom []byte) ([]byte, error) {
	if name == "utf-8" {
		return []byte(text), nil
	}
	if name == "utf-8-bom" {
		return append(append([]byte{}, bom...), []byte(text)...), nil
	}
	enc, err := LookupEncoding(name)
	if err != nil {
		return nil, err
	}
	encoded, _, err := transform.Bytes(enc.NewEncoder(), []byte(text))
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", name, err)
	}
	if len(bom) > 0 {
		encoded = append(append([]byte{}, bom...), encoded...)
	}
	return encoded, nil
}

func LookupEncoding(name string) (encoding.Encoding, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "gbk", "gb2312":
		name = "gb18030"
	case "shift_jis", "sjis":
		name = "shift-jis"
	}
	enc, err := htmlindex.Get(name)
	if err != nil || enc == nil {
		return nil, fmt.Errorf("unsupported encoding %q", name)
	}
	return enc, nil
}
