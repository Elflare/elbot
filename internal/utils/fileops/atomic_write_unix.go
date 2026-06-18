//go:build !windows

package fileops

import (
	"fmt"
	"os"
	"path/filepath"
)

func replaceFileAtomic(tmpPath, targetPath string) error {
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	if dir, err := os.Open(filepath.Dir(targetPath)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
