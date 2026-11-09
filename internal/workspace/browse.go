package workspace

import (
	"context"
	"io/fs"
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

type WorkspaceFile struct {
	// Name is the entry name in the current directory.
	Name string `json:"name"`
	// Path is the entry path relative to the workspace root.
	Path string `json:"path"`
	// Kind is either file or directory.
	Kind string `json:"kind"`
	// Size is the entry size in bytes.
	Size int64 `json:"size"`
}

type WorkspaceFileListing struct {
	// Side identifies the selected workspace.
	Side string `json:"side"`
	// Path is the listed directory relative to the workspace root.
	Path string `json:"path"`
	// Items contains regular files and directories in the listed directory.
	Items []WorkspaceFile `json:"items"`
}

func ListWorkspaceFiles(ctx context.Context, rootPath, relativePath string) (WorkspaceFileListing, error) {
	relativePath = filepath.Clean(filepath.FromSlash(relativePath))
	result := WorkspaceFileListing{Path: filepath.ToSlash(relativePath), Items: []WorkspaceFile{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return result, err
	}
	defer root.Close()
	dir, err := root.Open(relativePath)
	if err != nil {
		return result, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return result, err
		}
		kind := "file"
		if info.IsDir() {
			kind = "directory"
		} else if !info.Mode().IsRegular() {
			continue
		}
		result.Items = append(result.Items, WorkspaceFile{
			Name: entry.Name(), Path: filepath.ToSlash(filepath.Join(relativePath, entry.Name())),
			Kind: kind, Size: info.Size(),
		})
	}
	slices.SortFunc(result.Items, func(a, b WorkspaceFile) int {
		if a.Kind != b.Kind {
			if a.Kind == "directory" {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return result, nil
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
