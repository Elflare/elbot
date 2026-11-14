package workspace

import (
	"context"
	"elbot/internal/operationresult"
	"errors"
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

func ListWorkspaceFiles(ctx context.Context, rootPath, relativePath string) (operationresult.Result[WorkspaceFileListing], error) {
	relativePath = filepath.Clean(filepath.FromSlash(relativePath))
	listing := WorkspaceFileListing{Path: filepath.ToSlash(relativePath), Items: []WorkspaceFile{}}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[WorkspaceFileListing]{}, err
	}
	if !filepath.IsLocal(relativePath) {
		return operationresult.Result[WorkspaceFileListing]{Status: operationresult.InvalidArguments, Message: "workspace path must stay within the workspace"}, nil
	}
	if rootPath == "" {
		return operationresult.Result[WorkspaceFileListing]{Status: operationresult.Unavailable, Message: "workspace is not configured"}, nil
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return operationresult.Result[WorkspaceFileListing]{}, err
	}
	defer root.Close()
	dir, err := root.Open(relativePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return operationresult.Result[WorkspaceFileListing]{Status: operationresult.NotFound, Message: "workspace directory not found"}, nil
		}
		return operationresult.Result[WorkspaceFileListing]{}, err
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil {
		return operationresult.Result[WorkspaceFileListing]{}, err
	}
	if !info.IsDir() {
		return operationresult.Result[WorkspaceFileListing]{Status: operationresult.InvalidTarget, Message: "workspace path is not a directory"}, nil
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return operationresult.Result[WorkspaceFileListing]{}, err
	}
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return operationresult.Result[WorkspaceFileListing]{}, err
		}
		kind := "file"
		if info.IsDir() {
			kind = "directory"
		} else if !info.Mode().IsRegular() {
			continue
		}
		listing.Items = append(listing.Items, WorkspaceFile{
			Name: entry.Name(), Path: filepath.ToSlash(filepath.Join(relativePath, entry.Name())),
			Kind: kind, Size: info.Size(),
		})
	}
	slices.SortFunc(listing.Items, func(a, b WorkspaceFile) int {
		if a.Kind != b.Kind {
			if a.Kind == "directory" {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return operationresult.Result[WorkspaceFileListing]{Status: operationresult.Success, Data: listing}, nil
}
func ListDirectories(ctx context.Context, path string) (operationresult.Result[DirectoryListing], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[DirectoryListing]{}, err
	}
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = os.UserHomeDir()
		if err != nil {
			return operationresult.Result[DirectoryListing]{}, err
		}
	}
	checked, err := ValidateWorkspaceDir(path)
	if err != nil {
		return operationresult.Result[DirectoryListing]{}, err
	}
	if checked.Status != operationresult.Success {
		return operationresult.Result[DirectoryListing]{Status: checked.Status, Message: checked.Message}, nil
	}
	path = checked.Data
	entries, err := os.ReadDir(path)
	if err != nil {
		return operationresult.Result[DirectoryListing]{}, err
	}
	result := DirectoryListing{Path: path, Parent: filepath.Dir(path), Directories: []Directory{}}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		result.Directories = append(result.Directories, Directory{Name: entry.Name(), Path: filepath.Join(path, entry.Name())})
	}
	slices.SortFunc(result.Directories, func(a, b Directory) int { return strings.Compare(a.Name, b.Name) })
	return operationresult.Result[DirectoryListing]{Status: operationresult.Success, Data: result}, nil
}
