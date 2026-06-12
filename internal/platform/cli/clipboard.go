package cli

import "github.com/atotto/clipboard"

type clipboardWriter interface {
	WriteAll(string) error
}

type systemClipboardWriter struct{}

func (systemClipboardWriter) WriteAll(text string) error {
	return clipboard.WriteAll(text)
}
