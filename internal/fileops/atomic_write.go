package fileops

import (
	"fmt"
	"os"
	"path/filepath"

	"elbot/internal/operationresult"
)

func AtomicWriteFile(path string, data []byte, mode os.FileMode) error {
	_, err := atomicWriteFileResult(path, data, mode, nil)
	return err
}

// beforeCommit runs after temporary output is ready, immediately before the
// destination is changed. Symlink writes preserve the existing in-place rule.
func atomicWriteFileResult(path string, data []byte, mode os.FileMode, beforeCommit func() (operationresult.Result[struct{}], error)) (operationresult.Result[struct{}], error) {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if beforeCommit != nil {
			result, err := beforeCommit()
			if err != nil {
				return operationresult.Result[struct{}]{}, err
			}
			if result.Status != operationresult.Success {
				return result, nil
			}
		}
		if err := os.WriteFile(path, data, mode); err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return operationresult.Result[struct{}]{}, fmt.Errorf("create temp file: %w", err)
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
		return operationresult.Result[struct{}]{}, fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return operationresult.Result[struct{}]{}, fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return operationresult.Result[struct{}]{}, fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return operationresult.Result[struct{}]{}, fmt.Errorf("close temp file: %w", err)
	}
	if beforeCommit != nil {
		result, err := beforeCommit()
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		if result.Status != operationresult.Success {
			return result, nil
		}
	}
	if err := replaceFileAtomic(tmpPath, path); err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	cleanup = false
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}
