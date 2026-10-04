//go:build windows

package fileops

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	procMoveFileExW = kernel32.NewProc("MoveFileExW")
)

func replaceFileAtomic(tmpPath, targetPath string) error {
	from, err := syscall.UTF16PtrFromString(tmpPath)
	if err != nil {
		return fmt.Errorf("encode temp path: %w", err)
	}
	to, err := syscall.UTF16PtrFromString(targetPath)
	if err != nil {
		return fmt.Errorf("encode target path: %w", err)
	}
	const (
		movefileReplaceExisting = 0x1
		movefileWriteThrough    = 0x8
	)
	r1, _, callErr := procMoveFileExW.Call(
		uintptr(unsafe.Pointer(from)),
		uintptr(unsafe.Pointer(to)),
		uintptr(movefileReplaceExisting|movefileWriteThrough),
	)
	if r1 == 0 {
		if callErr != syscall.Errno(0) {
			return fmt.Errorf("replace file: %w", callErr)
		}
		return fmt.Errorf("replace file: MoveFileExW failed")
	}
	return nil
}
