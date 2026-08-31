package fileops

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const MaxFileSize = 2 * 1024 * 1024

type EditFileOptions struct {
	MaxInputBytes        int64
	MaxOutputBytes       int64
	DetailedDiffMaxBytes int64
}

type File struct {
	Path        string
	Bytes       []byte
	Text        string
	Encoding    string
	BOM         []byte
	LineEnding  string
	EndsNewline bool
}

type EditResult struct {
	DryRun         bool
	Path           string
	Created        bool
	Encoding       string
	RevisionBefore string
	RevisionAfter  string
	Diff           string
	NewBytes       []byte
}

func ReadFile(path, requestedEncoding string) (File, error) {
	return ReadFileWithLimit(path, requestedEncoding, MaxFileSize)
}

func ReadFileWithLimit(path, requestedEncoding string, maxBytes int64) (File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return File{}, fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() {
		return File{}, fmt.Errorf("path is a directory")
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return File{}, fmt.Errorf("file too large: %d bytes exceeds %d", info.Size(), maxBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("read file: %w", err)
	}
	if LooksBinary(data) {
		return File{}, fmt.Errorf("file appears to be binary")
	}
	text, encName, bom, err := DecodeBytes(data, requestedEncoding)
	if err != nil {
		return File{}, err
	}
	return File{Path: path, Bytes: data, Text: text, Encoding: encName, BOM: bom, LineEnding: DetectLineEnding(text), EndsNewline: strings.HasSuffix(text, "\n")}, nil
}

func ReadOrCreateFile(path, requestedEncoding string, create bool, hasExpectedRevision bool) (File, bool, error) {
	return ReadOrCreateFileWithLimit(path, requestedEncoding, create, hasExpectedRevision, MaxFileSize)
}

func ReadOrCreateFileWithLimit(path, requestedEncoding string, create bool, hasExpectedRevision bool, maxBytes int64) (File, bool, error) {
	file, err := ReadFileWithLimit(path, requestedEncoding, maxBytes)
	if err == nil {
		return file, false, nil
	}
	if !create || hasExpectedRevision || !errors.Is(err, os.ErrNotExist) {
		return File{}, false, err
	}
	encodingName := strings.ToLower(strings.TrimSpace(requestedEncoding))
	if encodingName == "" || encodingName == "auto" || encodingName == "utf8" {
		encodingName = "utf-8"
	}
	if encodingName == "utf-8-bom" {
		return File{Path: path, Encoding: "utf-8-bom", BOM: []byte{0xEF, 0xBB, 0xBF}, LineEnding: "\n"}, true, nil
	}
	if encodingName != "utf-8" {
		if _, err := LookupEncoding(encodingName); err != nil {
			return File{}, false, err
		}
	}
	return File{Path: path, Encoding: encodingName, LineEnding: "\n"}, true, nil
}

func EditFile(path, requestedEncoding, expectedRevision string, create, dryRun bool, contextLines int, edits []Edit) (EditResult, error) {
	return EditFileWithOptions(path, requestedEncoding, expectedRevision, create, dryRun, contextLines, edits, EditFileOptions{MaxInputBytes: MaxFileSize})
}

func EditFileWithOptions(path, requestedEncoding, expectedRevision string, create, dryRun bool, contextLines int, edits []Edit, options EditFileOptions) (EditResult, error) {
	expectedRevision = strings.TrimSpace(expectedRevision)
	if expectedRevision != "" {
		if len(expectedRevision) != contentRevisionBytes*2 {
			return EditResult{}, fmt.Errorf("expected_revision must be %d hexadecimal characters", contentRevisionBytes*2)
		}
		if _, err := hex.DecodeString(expectedRevision); err != nil {
			return EditResult{}, fmt.Errorf("expected_revision must be %d hexadecimal characters", contentRevisionBytes*2)
		}
	}
	file, created, err := ReadOrCreateFileWithLimit(path, requestedEncoding, create, expectedRevision != "", options.MaxInputBytes)
	if err != nil {
		return EditResult{}, err
	}
	oldRevision := ContentRevision(file.Bytes)
	if expectedRevision != "" && !strings.EqualFold(expectedRevision, oldRevision) {
		return EditResult{}, fmt.Errorf("file revision mismatch: current %s", oldRevision)
	}
	if !created && expectedRevision == "" && editsRequireRevision(edits) {
		return EditResult{}, fmt.Errorf("expected_revision is required for replace, insert, delete, and overwrite on existing files")
	}
	oldText := NormalizeEditText(file.Text)
	newText, err := ApplyEdits(oldText, edits)
	if err != nil {
		return EditResult{}, err
	}
	if newText == oldText {
		return EditResult{}, fmt.Errorf("edit produced no changes")
	}
	outputText := RestoreLineEndings(newText, file.LineEnding)
	newBytes, err := EncodeText(outputText, file.Encoding, file.BOM)
	if err != nil {
		return EditResult{}, err
	}
	if options.MaxOutputBytes > 0 && int64(len(newBytes)) > options.MaxOutputBytes {
		return EditResult{}, fmt.Errorf("edited file too large: %d bytes exceeds %d", len(newBytes), options.MaxOutputBytes)
	}
	contextLines = NormalizeContextLines(contextLines)
	result := EditResult{
		DryRun:         dryRun,
		Path:           file.Path,
		Created:        created,
		Encoding:       file.Encoding,
		RevisionBefore: oldRevision,
		RevisionAfter:  ContentRevision(newBytes),
		Diff:           editDiff(file.Path, oldText, newText, len(file.Bytes), len(newBytes), contextLines, options.DetailedDiffMaxBytes),
		NewBytes:       newBytes,
	}
	if dryRun {
		return result, nil
	}
	if created {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return EditResult{}, fmt.Errorf("create parent directory: %w", err)
		}
	}
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	} else if !created {
		return EditResult{}, fmt.Errorf("stat file: %w", err)
	}
	if err := AtomicWriteFile(path, newBytes, mode); err != nil {
		return EditResult{}, fmt.Errorf("write file: %w", err)
	}
	return result, nil
}

func WriteTextFile(path string, base File, text string) ([]byte, error) {
	outputText := RestoreLineEndings(text, base.LineEnding)
	newBytes, err := EncodeText(outputText, base.Encoding, base.BOM)
	if err != nil {
		return nil, err
	}
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	} else {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	if err := AtomicWriteFile(path, newBytes, mode); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}
	return newBytes, nil
}
