package fileops_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/fileops"
	"elbot/internal/operationresult"
)

func TestPreparedFileFreezesContentAndPreservesEncoding(t *testing.T) {
	for _, tc := range []struct {
		name, encoding, ending string
		bom                    []byte
	}{
		{"utf8", "utf-8", "\n", nil},
		{"utf8-bom-crlf", "utf-8-bom", "\r\n", []byte{0xEF, 0xBB, 0xBF}},
		{"utf16-crlf", "utf-16le", "\r\n", []byte{0xFF, 0xFE}},
		{"utf16be", "utf-16be", "\n", []byte{0xFE, 0xFF}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "memory.md")
			original, err := fileops.EncodeText("before"+tc.ending, tc.encoding, tc.bom)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			calls, replacement := 0, "after"
			prepared, err := fileops.PrepareFile(context.Background(), path, func(text string) (operationresult.Result[string], error) {
				calls++
				return operationresult.Result[string]{Status: operationresult.Success, Data: strings.ReplaceAll(text, "before", replacement)}, nil
			})
			if err != nil || prepared.Status != operationresult.Success {
				t.Fatalf("prepare: %+v %v", prepared, err)
			}
			preview := prepared.Data.Preview()
			if !preview.DryRun || preview.NewBytes != nil || !strings.Contains(preview.Diff, "+after") {
				t.Fatalf("preview = %+v", preview)
			}
			replacement = "unapproved"
			changed := preview
			changed.Path = filepath.Join(t.TempDir(), "other")
			changed.NewBytes = []byte("unapproved")
			result, err := prepared.Data.Commit(context.Background())
			if err != nil || result.Status != operationresult.Success {
				t.Fatalf("commit: %+v %v", result, err)
			}
			written, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want, err := fileops.EncodeText("after"+tc.ending, tc.encoding, tc.bom)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !bytes.Equal(written, want) || result.Data.DryRun || result.Data.RevisionAfter != preview.RevisionAfter || fileops.ContentRevision(written) != preview.RevisionAfter || result.Data.Diff != preview.Diff {
				t.Fatalf("calls=%d written=%x want=%x result=%+v preview=%+v", calls, written, want, result, preview)
			}
			if _, err := os.Stat(changed.Path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("preview redirected commit: %v", err)
			}
		})
	}
}

func TestPreparedFileRejectsChangedTargetOrRevisionAndCancellation(t *testing.T) {
	for _, operation := range []string{"external-write", "retarget", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			original, other, path := filepath.Join(dir, "original"), filepath.Join(dir, "other"), filepath.Join(dir, "link")
			for _, name := range []string{original, other} {
				if err := os.WriteFile(name, []byte("before"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(original, path); err != nil {
				t.Fatal(err)
			}
			prepared, err := fileops.PrepareFile(context.Background(), path, func(string) (operationresult.Result[string], error) {
				return operationresult.Result[string]{Status: operationresult.Success, Data: "after"}, nil
			})
			if err != nil || prepared.Status != operationresult.Success {
				t.Fatalf("prepare: %+v %v", prepared, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := "before"
			switch operation {
			case "external-write":
				want = "external"
				if err := os.WriteFile(original, []byte(want), 0600); err != nil {
					t.Fatal(err)
				}
			case "retarget":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				cancel()
			}
			result, err := prepared.Data.Commit(ctx)
			if operation == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
			} else if err != nil || result.Status != operationresult.Conflict || result.Message == "" {
				t.Fatalf("invalid snapshot: %+v %v", result, err)
			}
			for name, expected := range map[string]string{original: want, other: "before"} {
				data, err := os.ReadFile(name)
				if err != nil || string(data) != expected {
					t.Fatalf("%s = %q, want %q: %v", name, data, expected, err)
				}
			}
		})
	}
}

func TestPreparedFileWriteCreateAndConflict(t *testing.T) {
	for _, scenario := range []string{"new", "same", "appeared", "changed", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "nested", "file.txt")
			if scenario == "same" || scenario == "changed" {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("approved\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			prepared, err := fileops.PrepareFileWrite(t.Context(), path, "approved\n")
			if err != nil || prepared.Status != operationresult.Success {
				t.Fatalf("prepare: %+v %v", prepared, err)
			}
			if scenario == "same" && prepared.Data.Preview().DiffAvailable {
				t.Fatal("unchanged file has no displayable diff")
			}
			if scenario != "same" && scenario != "changed" {
				if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
					t.Fatalf("prepare created parent: %v", err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "appeared" || scenario == "changed" {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("external\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "canceled" {
				cancel()
			}
			result, err := prepared.Data.Commit(ctx)
			wantSuccess := scenario == "new" || scenario == "same"
			if scenario == "canceled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
			} else if err != nil || (result.Status == operationresult.Success) != wantSuccess || !wantSuccess && (result.Status != operationresult.Conflict || result.Message == "") {
				t.Fatalf("commit = %+v %v", result, err)
			}
			data, readErr := os.ReadFile(path)
			if scenario == "canceled" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("canceled write created file: %v", readErr)
				}
			} else {
				want := "external\n"
				if wantSuccess {
					want = "approved\n"
				}
				if readErr != nil || string(data) != want {
					t.Fatalf("file = %q, %v", data, readErr)
				}
			}
			if scenario == "same" {
				info, _ := os.Stat(path)
				if info.Mode().Perm() != 0600 {
					t.Fatal("mode changed")
				}
			}
		})
	}
}
