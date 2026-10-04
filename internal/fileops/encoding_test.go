package fileops

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileAutoDetectsUTF16WithBOM(t *testing.T) {
	for _, tt := range []struct {
		name string
		bom  []byte
		enc  string
	}{
		{name: "utf-16le", bom: []byte{0xFF, 0xFE}, enc: "utf-16le"},
		{name: "utf-16be", bom: []byte{0xFE, 0xFF}, enc: "utf-16be"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sample.txt")
			encoded, err := EncodeText("alpha\n中文\n", tt.name, nil)
			if err != nil {
				t.Fatal(err)
			}
			data := append(append([]byte{}, tt.bom...), encoded...)
			if err := os.WriteFile(path, data, 0644); err != nil {
				t.Fatal(err)
			}

			file, err := ReadFile(path, "")
			if err != nil {
				t.Fatal(err)
			}
			if file.Text != "alpha\n中文\n" {
				t.Fatalf("text = %q", file.Text)
			}
			if file.Encoding != tt.name {
				t.Fatalf("encoding = %q, want %q", file.Encoding, tt.name)
			}
			if !bytes.Equal(file.BOM, tt.bom) {
				t.Fatalf("BOM = %x, want %x", file.BOM, tt.bom)
			}
		})
	}
}

func TestReadFileExplicitEncodingAllowsNUL(t *testing.T) {
	for _, encoding := range []string{"utf-16le", "utf-16be", "gb18030", "big5", "shift_jis"} {
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
			if file.Text != want {
				t.Fatalf("text = %q, want %q", file.Text, want)
			}
		})
	}
}

func TestReadFileKeepsUTF8BinaryProtection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\x00beta"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadFile(path, "")
	if err == nil || !strings.Contains(err.Error(), "file appears to be binary") {
		t.Fatalf("expected binary error, got %v", err)
	}
}

func TestEditFilePreservesEncodingAndBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	bom := []byte{0xFF, 0xFE}
	encoded, err := EncodeText("alpha\nbeta\n", "utf-16le", nil)
	if err != nil {
		t.Fatal(err)
	}
	original := append(append([]byte{}, bom...), encoded...)
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}

	result, err := EditFile(path, "", ContentRevision(original), false, false, 0, []Edit{{
		Operation: "replace_text",
		OldText:   "beta",
		NewText:   stringPtr("gamma"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Encoding != "utf-16le" {
		t.Fatalf("encoding = %q", result.Encoding)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantText := "alpha\ngamma\n"
	wantEncoded, err := EncodeText(wantText, "utf-16le", bom)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wantEncoded) {
		t.Fatalf("encoded content = %x, want %x", got, wantEncoded)
	}
}

func stringPtr(value string) *string {
	return &value
}
