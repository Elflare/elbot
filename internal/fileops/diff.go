package fileops

import "github.com/aymanbagabas/go-udiff"

// displayFileDiff limits preview work, not the file operation itself.
func displayFileDiff(oldBytes, newBytes int) bool {
	const maxDiffFileBytes = 8 * 1024 * 1024
	return oldBytes <= maxDiffFileBytes && newBytes <= maxDiffFileBytes
}

// editDiff renders all changes in the prepared text without truncating hunks.
func editDiff(path, oldText, newText string, contextLines int) (string, error) {
	return udiff.ToUnified(path, path, oldText, udiff.Lines(oldText, newText), contextLines)
}
