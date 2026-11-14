package fileops

import (
	"os"
	"path/filepath"
	"testing"

	"elbot/internal/operationresult"
)

func TestReadFileNULDependsOnEncoding(t *testing.T) {
	for _, encoding := range []string{"utf-8", "utf-16le", "utf-16be", "gb18030", "big5", "shift_jis"} {
		t.Run(encoding, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sample.txt")
			want := "alpha\x00beta"
			data, err := EncodeText(want, encoding, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0644); err != nil {
				t.Fatal(err)
			}
			file, err := ReadFile(path, encoding)
			if err != nil {
				t.Fatal(err)
			}
			if encoding == "utf-8" {
				if file.Status != operationresult.InvalidTarget || file.Message != "file appears to be binary" {
					t.Fatalf("binary rejection: %+v", file)
				}
			} else if file.Status != operationresult.Success || file.Data.Text != want {
				t.Fatalf("decoded: %+v, want %q", file, want)
			}
		})
	}
}
