package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

func (m *Supervisor) ListFiles(ctx context.Context, req ListFilesRequest) (ListFilesResult, error) {
	result := ListFilesResult{Root: req.Root, Path: req.Path, Items: []FileEntry{}}
	if err := authorize(ctx, true, true); err != nil {
		return result, err
	}
	if req.Root == "" {
		if req.Path != "" {
			return result, invalid("列举根目录时不能指定 path")
		}
		names := []string{"config"}
		for name := range m.roots {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			result.Items = append(result.Items, FileEntry{Name: name, Path: name, Kind: "directory", Exists: true, Writable: name != "logs"})
		}
		return result, nil
	}
	path, err := managedPath(req.Path, true)
	if err != nil {
		return result, err
	}
	result.Path = filepath.ToSlash(path)
	var fixed []string
	if req.Root == "config" {
		if path != "." {
			return result, invalid("config 只有固定文件，没有子目录")
		}
		for name := range m.configFiles {
			fixed = append(fixed, name)
		}
		slices.Sort(fixed)
	} else if req.Root == "memory" && path == "." {
		fixed = []string{"long_memory", "memories.toml"}
	}
	if fixed != nil {
		for _, name := range fixed {
			entry, err := m.fixedFileEntry(ctx, req.Root, name)
			if err != nil {
				return result, err
			}
			result.Items = append(result.Items, entry)
		}
		return result, nil
	}
	root, name, err := m.openManaged(req.Root, path, false, true)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, ClassifyError(err)
	}
	defer root.Close()
	dir, err := root.Open(name)
	if err != nil {
		return result, ClassifyError(err)
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return result, ClassifyError(err)
	}
	for _, entry := range entries {
		child := filepath.Join(name, entry.Name())
		if !allowedManagedPath(child) {
			continue
		}
		info, err := root.Stat(child)
		if err != nil {
			return result, ClassifyError(err)
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			continue
		}
		kind := "file"
		if info.IsDir() {
			kind = "directory"
		}
		result.Items = append(result.Items, FileEntry{Name: entry.Name(), Path: filepath.ToSlash(filepath.Join(path, entry.Name())), Kind: kind, Size: info.Size(), Exists: true, Writable: req.Root != "logs"})
	}
	slices.SortFunc(result.Items, func(a, b FileEntry) int {
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

func (m *Supervisor) ReadFile(ctx context.Context, req ReadFileRequest) (ReadFileResult, error) {
	result := ReadFileResult{Root: req.Root, Path: req.Path}
	if err := authorize(ctx, true, true); err != nil {
		return result, err
	}
	if req.Offset < 0 || req.Limit < 0 {
		return result, invalid("文件读取偏移或字节数无效")
	}
	m.filesMu.RLock()
	defer m.filesMu.RUnlock()
	root, name, err := m.openManaged(req.Root, req.Path, false, false)
	if err != nil {
		return result, ClassifyError(err)
	}
	defer root.Close()
	info, err := root.Stat(name)
	if err != nil {
		return result, ClassifyError(err)
	}
	if !info.Mode().IsRegular() {
		return result, &Error{Code: "invalid_target", Message: "目标不是普通文件"}
	}
	var data []byte
	if req.Offset != 0 || req.Limit != 0 {
		file, err := root.Open(name)
		if err != nil {
			return result, ClassifyError(err)
		}
		defer file.Close()
		if _, err := file.Seek(req.Offset, io.SeekStart); err != nil {
			return result, ClassifyError(err)
		}
		var reader io.Reader = file
		if req.Limit != 0 {
			reader = io.LimitReader(file, req.Limit)
		}
		data, err = io.ReadAll(reader)
		if err != nil {
			return result, ClassifyError(err)
		}
	} else {
		data, err = root.ReadFile(name)
		if err != nil {
			return result, ClassifyError(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return result, ClassifyError(err)
	}
	result.Content = string(data)
	result.Revision = fileRevision(data)
	result.Size = info.Size()
	result.Offset = req.Offset
	result.NextOffset = req.Offset + int64(len(data))
	result.Truncated = result.NextOffset < result.Size
	return result, nil
}

// WriteFile 原样写入前端提交的 UTF-8 文本，不转换编码、换行或创建撤销记录。
func (m *Supervisor) WriteFile(ctx context.Context, req WriteFileRequest) (WriteFileResult, error) {
	result := WriteFileResult{Root: req.Root, Path: req.Path}
	if err := authorize(ctx, true, true); err != nil {
		return result, err
	}
	if req.Root == "logs" {
		return result, &Error{Code: "permission_denied", Message: "日志文件只读"}
	}
	if !utf8.ValidString(req.Content) {
		return result, invalid("content 必须是 UTF-8 文本")
	}
	if req.Create && req.ExpectedRevision != "" {
		return result, invalid("创建文件不能提供 expected_revision")
	}
	if !req.Create && req.ExpectedRevision == "" {
		return result, invalid("保存已有文件必须提供 expected_revision")
	}
	m.filesMu.Lock()
	defer m.filesMu.Unlock()
	if err := ctx.Err(); err != nil {
		return result, ClassifyError(err)
	}
	root, name, err := m.openManaged(req.Root, req.Path, req.Create, false)
	if err != nil {
		return result, ClassifyError(err)
	}
	defer root.Close()
	info, err := root.Stat(name)
	created := errors.Is(err, fs.ErrNotExist)
	if err != nil && !created {
		return result, ClassifyError(err)
	}
	if req.Create && !created {
		return result, &Error{Code: "file_changed", Message: "目标文件已存在"}
	}
	if !req.Create && created {
		return result, ClassifyError(fs.ErrNotExist)
	}
	if !created {
		if !info.Mode().IsRegular() {
			return result, &Error{Code: "invalid_target", Message: "目标不是普通文件"}
		}
		data, err := root.ReadFile(name)
		if err != nil {
			return result, ClassifyError(err)
		}
		if fileRevision(data) != req.ExpectedRevision {
			return result, &Error{Code: "file_changed", Message: "文件已经改变，请重新读取"}
		}
	} else if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return result, ClassifyError(err)
	}
	// 日志只读约束作用于实际目标，也覆盖来自其他逻辑根的路径或符号链接。
	target := filepath.Join(root.Name(), name)
	if created {
		parent, err := filepath.EvalSymlinks(filepath.Dir(target))
		if err != nil {
			return result, ClassifyError(err)
		}
		target = filepath.Join(parent, filepath.Base(target))
	} else {
		target, err = filepath.EvalSymlinks(target)
		if err != nil {
			return result, ClassifyError(err)
		}
	}
	if m.isLogTarget(target) {
		return result, &Error{Code: "permission_denied", Message: "日志文件只读"}
	}
	flags := os.O_WRONLY | os.O_TRUNC
	if created {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	file, err := root.OpenFile(name, flags, 0o600)
	if err != nil {
		return result, ClassifyError(err)
	}
	_, writeErr := io.WriteString(file, req.Content)
	closeErr := file.Close()
	if writeErr != nil {
		return result, ClassifyError(writeErr)
	}
	if closeErr != nil {
		return result, ClassifyError(closeErr)
	}
	result.Created = created
	result.Revision = fileRevision([]byte(req.Content))
	return result, nil
}

func fileRevision(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (m *Supervisor) isLogTarget(target string) bool {
	base := m.roots["logs"]
	if base == "" {
		return false
	}
	within := func(base string) bool {
		relative, err := filepath.Rel(base, target)
		return err == nil && filepath.IsLocal(relative)
	}
	if within(base) {
		return true
	}
	resolved, err := filepath.EvalSymlinks(base)
	return err == nil && within(resolved)
}

func managedPath(path string, dir bool) (string, error) {
	if path == "" && dir {
		return ".", nil
	}
	if strings.ContainsAny(path, "\\\x00") || !filepath.IsLocal(filepath.FromSlash(path)) {
		return "", invalid("path 必须是受管根内的相对路径")
	}
	path = filepath.Clean(filepath.FromSlash(path))
	if path == "." && !dir {
		return "", invalid("必须指定文件 path")
	}
	if !allowedManagedPath(path) {
		return "", &Error{Code: "invalid_target", Message: "该路径不是受管文本文件"}
	}
	return path, nil
}

func allowedManagedPath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if slices.Contains([]string{".git", ".cache", "__pycache__", "node_modules"}, part) {
			return false
		}
	}
	return !slices.Contains([]string{".db", ".sqlite", ".sqlite3", ".exe", ".dll", ".so", ".a", ".o", ".pyc"}, strings.ToLower(filepath.Ext(path)))
}

// openManaged 只接受逻辑根和相对路径；所有实际打开均通过 os.Root 限定范围。
func (m *Supervisor) openManaged(kind, path string, create, dir bool) (*os.Root, string, error) {
	name, err := managedPath(path, dir)
	if err != nil {
		return nil, "", err
	}
	if kind == "config" {
		actual, ok := m.configFiles[name]
		if !ok {
			return nil, "", &Error{Code: "not_found", Message: "不是已登记的配置文件"}
		}
		root, err := openManagedRoot(filepath.Dir(actual), create)
		return root, filepath.Base(actual), err
	}
	base, ok := m.roots[kind]
	if !ok {
		return nil, "", &Error{Code: "not_found", Message: "受管文件根不存在"}
	}
	if kind == "logs" {
		root, err := os.OpenRoot(base)
		return root, name, err
	}
	configDir := m.roots["memory"]
	child := kind
	if kind == "memory" {
		if name == "memories.toml" {
			root, err := os.OpenRoot(configDir)
			return root, name, err
		}
		if name != "long_memory" && !strings.HasPrefix(filepath.ToSlash(name), "long_memory/") {
			return nil, "", &Error{Code: "invalid_target", Message: "不是受管记忆文件"}
		}
		child = "long_memory"
		name = strings.TrimPrefix(strings.TrimPrefix(filepath.ToSlash(name), "long_memory"), "/")
		if name == "" {
			name = "."
		}
		name = filepath.FromSlash(name)
	}
	if kind != "memory" {
		configDir, child = filepath.Dir(base), filepath.Base(base)
	}
	root, err := openManagedChildRoot(configDir, child, create)
	return root, name, err
}

// 固定子目录不能通过根位置的符号链接扩大范围。
func openManagedChildRoot(base, child string, create bool) (*os.Root, error) {
	parent, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	info, err := parent.Lstat(child)
	if errors.Is(err, fs.ErrNotExist) && create {
		if err := parent.Mkdir(child, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		info, err = parent.Lstat(child)
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, &Error{Code: "invalid_target", Message: "受管根必须是普通目录"}
	}
	root, err := parent.OpenRoot(child)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	if !os.SameFile(info, opened) {
		root.Close()
		return nil, &Error{Code: "file_changed", Message: "受管目录已经改变"}
	}
	return root, nil
}

func openManagedRoot(path string, create bool) (*os.Root, error) {
	root, err := os.OpenRoot(path)
	if err == nil || !create || !errors.Is(err, fs.ErrNotExist) {
		return root, err
	}
	ancestor := filepath.Dir(path)
	for {
		root, err = os.OpenRoot(ancestor)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) || filepath.Dir(ancestor) == ancestor {
			return nil, err
		}
		ancestor = filepath.Dir(ancestor)
	}
	defer root.Close()
	relative, err := filepath.Rel(ancestor, path)
	if err != nil {
		return nil, err
	}
	if err := root.MkdirAll(relative, 0o755); err != nil {
		return nil, err
	}
	return root.OpenRoot(relative)
}

func (m *Supervisor) fixedFileEntry(ctx context.Context, kind, name string) (FileEntry, error) {
	entry := FileEntry{Name: name, Path: name, Kind: "file", Writable: true}
	if name == "long_memory" {
		entry.Kind = "directory"
	}
	if err := ctx.Err(); err != nil {
		return entry, ClassifyError(err)
	}
	root, path, err := m.openManaged(kind, name, false, true)
	if errors.Is(err, fs.ErrNotExist) {
		return entry, nil
	}
	if err != nil {
		return entry, ClassifyError(err)
	}
	defer root.Close()
	info, err := root.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return entry, nil
	}
	if err != nil {
		return entry, ClassifyError(err)
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return entry, &Error{Code: "invalid_target", Message: "目标不是普通文件"}
	}
	entry.Exists = true
	entry.Size = info.Size()
	if info.IsDir() {
		entry.Kind = "directory"
	}
	return entry, nil
}
