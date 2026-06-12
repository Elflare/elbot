package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type recordingClipboardWriter struct {
	text  string
	calls int
	err   error
}

func (w *recordingClipboardWriter) WriteAll(text string) error {
	w.calls++
	w.text = text
	return w.err
}

func TestAutoClipboardUsesSystemFirstOutsideRemoteSession(t *testing.T) {
	system := &recordingClipboardWriter{}
	osc := &recordingClipboardWriter{}
	writer := autoClipboardWriter{system: system, osc52: osc, env: emptyEnv}

	if err := writer.WriteAll("hello"); err != nil {
		t.Fatalf("WriteAll error = %v", err)
	}
	if system.calls != 1 || system.text != "hello" {
		t.Fatalf("system calls=%d text=%q", system.calls, system.text)
	}
	if osc.calls != 0 {
		t.Fatalf("osc52 should not be called, calls=%d", osc.calls)
	}
}

func TestAutoClipboardFallsBackToOSC52WhenSystemFails(t *testing.T) {
	system := &recordingClipboardWriter{err: errors.New("no clipboard")}
	osc := &recordingClipboardWriter{}
	writer := autoClipboardWriter{system: system, osc52: osc, env: emptyEnv}

	if err := writer.WriteAll("hello"); err != nil {
		t.Fatalf("WriteAll error = %v", err)
	}
	if system.calls != 1 {
		t.Fatalf("system calls=%d, want 1", system.calls)
	}
	if osc.calls != 1 || osc.text != "hello" {
		t.Fatalf("osc52 calls=%d text=%q", osc.calls, osc.text)
	}
}

func TestAutoClipboardUsesOSC52FirstInRemoteSession(t *testing.T) {
	system := &recordingClipboardWriter{}
	osc := &recordingClipboardWriter{}
	writer := autoClipboardWriter{system: system, osc52: osc, env: mapEnv(map[string]string{"SSH_TTY": "/dev/pts/1"})}

	if err := writer.WriteAll("hello"); err != nil {
		t.Fatalf("WriteAll error = %v", err)
	}
	if osc.calls != 1 || osc.text != "hello" {
		t.Fatalf("osc52 calls=%d text=%q", osc.calls, osc.text)
	}
	if system.calls != 0 {
		t.Fatalf("system should not be called, calls=%d", system.calls)
	}
}

func TestAutoClipboardFallsBackToSystemWhenRemoteOSC52Fails(t *testing.T) {
	system := &recordingClipboardWriter{}
	osc := &recordingClipboardWriter{err: errors.New("osc failed")}
	writer := autoClipboardWriter{system: system, osc52: osc, env: mapEnv(map[string]string{"TMUX": "1"})}

	if err := writer.WriteAll("hello"); err != nil {
		t.Fatalf("WriteAll error = %v", err)
	}
	if osc.calls != 1 {
		t.Fatalf("osc52 calls=%d, want 1", osc.calls)
	}
	if system.calls != 1 || system.text != "hello" {
		t.Fatalf("system calls=%d text=%q", system.calls, system.text)
	}
}

func TestOSC52ClipboardWritesSequence(t *testing.T) {
	var out bytes.Buffer
	writer := osc52ClipboardWriter{out: &out, maxBytes: 1024}

	if err := writer.WriteAll("hello"); err != nil {
		t.Fatalf("WriteAll error = %v", err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "\x1b]52;c;") || !strings.HasSuffix(got, "\x07") {
		t.Fatalf("OSC52 sequence = %q", got)
	}
}

func TestOSC52ClipboardRejectsOversizedText(t *testing.T) {
	var out bytes.Buffer
	writer := osc52ClipboardWriter{out: &out, maxBytes: 3}

	if err := writer.WriteAll("hello"); err == nil {
		t.Fatal("WriteAll error = nil, want oversized error")
	}
	if out.Len() != 0 {
		t.Fatalf("output len=%d, want 0", out.Len())
	}
}

func emptyEnv(string) string { return "" }

func mapEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
