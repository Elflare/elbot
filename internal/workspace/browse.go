package workspace

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type Directory struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type DirectoryListing struct {
	Path        string      `json:"path"`
	Parent      string      `json:"parent"`
	Directories []Directory `json:"directories"`
}

func ListDirectories(ctx context.Context, path string) (DirectoryListing, error) {
	if err := ctx.Err(); err != nil {
		return DirectoryListing{}, err
	}
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = os.UserHomeDir()
		if err != nil {
			return DirectoryListing{}, err
		}
	}
	path, err := ValidateWorkspaceDir(path)
	if err != nil {
		return DirectoryListing{}, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return DirectoryListing{}, err
	}
	result := DirectoryListing{Path: path, Parent: filepath.Dir(path), Directories: []Directory{}}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		result.Directories = append(result.Directories, Directory{Name: entry.Name(), Path: filepath.Join(path, entry.Name())})
	}
	slices.SortFunc(result.Directories, func(a, b Directory) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}
