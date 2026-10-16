package fileops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileWithRevision commits prepared bytes only while the existing target
// still matches the approved revision. It uses the same bounded source size as
// the memory and skill editors; it does not generate another diff.
func WriteFileWithRevision(ctx context.Context, path, target, expectedRevision string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkFileTarget(path, target, false); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > MaxFileSize {
		return fmt.Errorf("file too large: %d bytes exceeds %d", info.Size(), MaxFileSize)
	}
	result, err := readRevisionBytes(path, info.Size())
	if err != nil {
		return err
	}
	if ContentRevision(result) != expectedRevision {
		return fmt.Errorf("file changed after preflight")
	}
	return atomicWriteFile(path, data, info.Mode(), func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := checkFileTarget(path, target, false); err != nil {
			return err
		}
		return checkFileState(path, false, info)
	})
}

func AtomicWriteFile(path string, data []byte, mode os.FileMode) error {
	return atomicWriteFile(path, data, mode, nil)
}

// beforeCommit runs after temporary output is ready, immediately before the
// destination is changed. Symlink writes preserve the existing in-place rule.
func atomicWriteFile(path string, data []byte, mode os.FileMode, beforeCommit func() error) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if beforeCommit != nil {
			if err := beforeCommit(); err != nil {
				return err
			}
		}
		return os.WriteFile(path, data, mode)
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if beforeCommit != nil {
		if err := beforeCommit(); err != nil {
			return err
		}
	}
	if err := replaceFileAtomic(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
