package fileops

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"elbot/internal/operationresult"
)

const MaxFileSize = 2 * 1024 * 1024

type EditFileOptions struct {
	MaxInputBytes        int64
	MaxOutputBytes       int64
	DetailedDiffMaxBytes int64
	beforeWrite          func(File, bool, os.FileMode) (operationresult.Result[struct{}], error) // 写入前的业务准入与最终复核。
}

type File struct {
	Path       string
	Bytes      []byte
	Text       string
	Encoding   string
	BOM        []byte
	LineEnding string
}

type EditResult struct {
	DryRun         bool
	Path           string
	Created        bool
	Encoding       string
	RevisionBefore string
	RevisionAfter  string
	Diff           string
	DiffAvailable  bool // Diff 是否包含可展示的完整差异 hunk。
	// NewBytes contains the edited file bytes; confirmation previews omit them.
	NewBytes          []byte
	RollbackAvailable bool
	RollbackEvicted   int
}

func ReadFile(path, requestedEncoding string) (operationresult.Result[File], error) {
	return ReadFileWithLimit(path, requestedEncoding, MaxFileSize)
}

func ReadFileWithLimit(path, requestedEncoding string, maxBytes int64) (operationresult.Result[File], error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return operationresult.Result[File]{Status: operationresult.NotFound, Message: fmt.Sprintf("file %q not found", path)}, nil
	}
	if err != nil {
		return operationresult.Result[File]{}, fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() {
		return operationresult.Result[File]{Status: operationresult.InvalidTarget, Message: "path is a directory"}, nil
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return operationresult.Result[File]{Status: operationresult.Rejected, Message: fmt.Sprintf("file too large: %d bytes exceeds %d", info.Size(), maxBytes)}, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return operationresult.Result[File]{Status: operationresult.NotFound, Message: fmt.Sprintf("file %q not found", path)}, nil
	}
	if err != nil {
		return operationresult.Result[File]{}, fmt.Errorf("read file: %w", err)
	}
	text, encName, bom, err := DecodeBytes(data, requestedEncoding)
	if err != nil {
		return operationresult.Result[File]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	if isUTF8Encoding(encName) && LooksBinaryUTF8(text) {
		return operationresult.Result[File]{Status: operationresult.InvalidTarget, Message: "file appears to be binary"}, nil
	}
	return operationresult.Result[File]{Status: operationresult.Success, Data: File{Path: path, Bytes: data, Text: text, Encoding: encName, BOM: bom, LineEnding: DetectLineEnding(text)}}, nil
}

func ReadOrCreateFile(path, requestedEncoding string, create bool, hasExpectedRevision bool) (operationresult.Result[File], bool, error) {
	return ReadOrCreateFileWithLimit(path, requestedEncoding, create, hasExpectedRevision, MaxFileSize)
}

func ReadOrCreateFileWithLimit(path, requestedEncoding string, create bool, hasExpectedRevision bool, maxBytes int64) (operationresult.Result[File], bool, error) {
	file, err := ReadFileWithLimit(path, requestedEncoding, maxBytes)
	if err != nil {
		return operationresult.Result[File]{}, false, err
	}
	if file.Status == operationresult.Success {
		return file, false, nil
	}
	if !create || hasExpectedRevision || file.Status != operationresult.NotFound {
		return file, false, nil
	}
	encodingName := strings.ToLower(strings.TrimSpace(requestedEncoding))
	if encodingName == "" || encodingName == "auto" || encodingName == "utf8" {
		encodingName = "utf-8"
	}
	if encodingName == "utf-8-bom" {
		return operationresult.Result[File]{Status: operationresult.Success, Data: File{Path: path, Encoding: "utf-8-bom", BOM: []byte{0xEF, 0xBB, 0xBF}, LineEnding: "\n"}}, true, nil
	}
	if encodingName != "utf-8" {
		if _, err := LookupEncoding(encodingName); err != nil {
			return operationresult.Result[File]{Status: operationresult.InvalidArguments, Message: err.Error()}, false, nil
		}
	}
	return operationresult.Result[File]{Status: operationresult.Success, Data: File{Path: path, Encoding: encodingName, LineEnding: "\n"}}, true, nil
}

func EditFile(path, requestedEncoding, expectedRevision string, create, dryRun bool, contextLines int, edits []Edit) (operationresult.Result[EditResult], error) {
	return EditFileWithOptions(path, requestedEncoding, expectedRevision, create, dryRun, contextLines, edits, EditFileOptions{MaxInputBytes: MaxFileSize})
}

func EditFileWithOptions(path, requestedEncoding, expectedRevision string, create, dryRun bool, contextLines int, edits []Edit, options EditFileOptions) (operationresult.Result[EditResult], error) {
	return editFileWithResult(path, requestedEncoding, expectedRevision, create, dryRun, contextLines, edits, options, nil)
}

// editFileWithResult uses the approved bytes when prepared is non-nil.
func editFileWithResult(path, requestedEncoding, expectedRevision string, create, dryRun bool, contextLines int, edits []Edit, options EditFileOptions, prepared *EditResult) (operationresult.Result[EditResult], error) {
	expectedRevision = strings.TrimSpace(expectedRevision)
	if prepared == nil && expectedRevision != "" {
		if len(expectedRevision) != contentRevisionBytes*2 {
			return operationresult.Result[EditResult]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("expected_revision must be %d hexadecimal characters", contentRevisionBytes*2)}, nil
		}
		if _, err := hex.DecodeString(expectedRevision); err != nil {
			return operationresult.Result[EditResult]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("expected_revision must be %d hexadecimal characters", contentRevisionBytes*2)}, nil
		}
	}
	loaded, created, err := ReadOrCreateFileWithLimit(path, requestedEncoding, create, expectedRevision != "", options.MaxInputBytes)
	if err != nil {
		return operationresult.Result[EditResult]{}, err
	}
	if loaded.Status != operationresult.Success {
		return operationresult.Result[EditResult]{Status: loaded.Status, Message: loaded.Message}, nil
	}
	file := loaded.Data
	oldRevision := ContentRevision(file.Bytes)
	var result EditResult
	if prepared != nil {
		if prepared.Created != created || prepared.RevisionBefore != oldRevision {
			return operationresult.Result[EditResult]{Status: operationresult.Conflict, Message: "file changed after preflight"}, nil
		}
		result = *prepared
	} else {
		if expectedRevision != "" && !strings.EqualFold(expectedRevision, oldRevision) {
			return operationresult.Result[EditResult]{Status: operationresult.Conflict, Message: fmt.Sprintf("file revision mismatch: current %s", oldRevision)}, nil
		}
		if !created && expectedRevision == "" && editsRequireRevision(edits) {
			return operationresult.Result[EditResult]{Status: operationresult.InvalidArguments, Message: "expected_revision is required for replace, insert, delete, and overwrite on existing files"}, nil
		}
		newText, err := ApplyEdits(file.Text, edits)
		if err != nil {
			return operationresult.Result[EditResult]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
		}
		prepared := prepareFileResult(file, created, newText, contextLines, options)
		if prepared.Status != operationresult.Success {
			return prepared, nil
		}
		result = prepared.Data
	}
	result.DryRun = dryRun
	if dryRun {
		return operationresult.Result[EditResult]{Status: operationresult.Success, Data: result}, nil
	}
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	} else if !created && errors.Is(err, os.ErrNotExist) {
		return operationresult.Result[EditResult]{Status: operationresult.NotFound, Message: fmt.Sprintf("file %q not found", path)}, nil
	} else if !created || !errors.Is(err, os.ErrNotExist) {
		return operationresult.Result[EditResult]{}, fmt.Errorf("stat file: %w", err)
	}
	if created {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return operationresult.Result[EditResult]{}, fmt.Errorf("create parent directory: %w", err)
		}
	}
	commit, err := atomicWriteFileResult(path, result.NewBytes, mode, func() (operationresult.Result[struct{}], error) {
		if options.beforeWrite != nil {
			return options.beforeWrite(file, created, mode)
		}
		return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	})
	if err != nil {
		return operationresult.Result[EditResult]{}, fmt.Errorf("write file: %w", err)
	}
	if commit.Status != operationresult.Success {
		return operationresult.Result[EditResult]{Status: commit.Status, Message: commit.Message}, nil
	}
	if prepared != nil {
		result.NewBytes = bytes.Clone(result.NewBytes)
	}
	return operationresult.Result[EditResult]{Status: operationresult.Success, Data: result}, nil
}
