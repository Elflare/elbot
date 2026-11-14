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

	"elbot/internal/control/share"
	"elbot/internal/operationresult"
)

func (m *Supervisor) ListFiles(ctx context.Context, req ListFilesRequest) (operationresult.Result[ListFilesResult], error) {
	result := ListFilesResult{Root: req.Root, Path: req.Path, Items: []FileEntry{}}
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[ListFilesResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[ListFilesResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	if req.Root == "" {
		if req.Path != "" {
			return operationresult.Result[ListFilesResult]{Status: operationresult.InvalidArguments, Message: "列举根目录时不能指定 path"}, nil
		}
		names := []string{"config"}
		for name := range m.roots {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			result.Items = append(result.Items, FileEntry{Name: name, Path: name, Kind: "directory", Exists: true, Writable: name != "logs"})
		}
		return operationresult.Result[ListFilesResult]{Status: operationresult.Success, Data: result}, nil
	}
	checked := managedPath(req.Path, true)
	if checked.Status != operationresult.Success {
		return operationresult.Result[ListFilesResult]{Status: checked.Status, Message: checked.Message}, nil
	}
	path := checked.Data
	result.Path = filepath.ToSlash(path)
	var fixed []string
	if req.Root == "config" {
		if path != "." {
			return operationresult.Result[ListFilesResult]{Status: operationresult.InvalidArguments, Message: "config 只有固定文件，没有子目录"}, nil
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
				return operationresult.Result[ListFilesResult]{}, err
			}
			if entry.Status != operationresult.Success {
				return operationresult.Result[ListFilesResult]{Status: entry.Status, Message: entry.Message}, nil
			}
			result.Items = append(result.Items, entry.Data)
		}
		return operationresult.Result[ListFilesResult]{Status: operationresult.Success, Data: result}, nil
	}
	opened, name, err := m.openManaged(req.Root, path, false, true)
	if errors.Is(err, fs.ErrNotExist) {
		return operationresult.Result[ListFilesResult]{Status: operationresult.Success, Data: result}, nil
	}
	if err != nil {
		return operationresult.Result[ListFilesResult]{}, err
	}
	if opened.Status != operationresult.Success {
		return operationresult.Result[ListFilesResult]{Status: opened.Status, Message: opened.Message}, nil
	}
	root := opened.Data
	defer root.Close()
	dir, err := root.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return operationresult.Result[ListFilesResult]{Status: operationresult.NotFound, Message: "目标目录不存在"}, nil
	}
	if err != nil {
		return operationresult.Result[ListFilesResult]{}, err
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil {
		return operationresult.Result[ListFilesResult]{}, err
	}
	if !info.IsDir() {
		return operationresult.Result[ListFilesResult]{Status: operationresult.InvalidTarget, Message: "目标不是目录"}, nil
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return operationresult.Result[ListFilesResult]{}, err
	}
	for _, entry := range entries {
		child := filepath.Join(name, entry.Name())
		if !allowedManagedPath(child) {
			continue
		}
		info, err := root.Stat(child)
		if err != nil {
			return operationresult.Result[ListFilesResult]{}, err
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
	return operationresult.Result[ListFilesResult]{Status: operationresult.Success, Data: result}, nil
}

func (m *Supervisor) ReadFile(ctx context.Context, req ReadFileRequest) (operationresult.Result[ReadFileResult], error) {
	result := ReadFileResult{Root: req.Root, Path: req.Path}
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[ReadFileResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[ReadFileResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	if req.Offset < 0 || req.Limit < 0 {
		return operationresult.Result[ReadFileResult]{Status: operationresult.InvalidArguments, Message: "文件读取偏移或字节数无效"}, nil
	}
	m.filesMu.RLock()
	defer m.filesMu.RUnlock()
	opened, name, err := m.openManaged(req.Root, req.Path, false, false)
	if errors.Is(err, fs.ErrNotExist) {
		return operationresult.Result[ReadFileResult]{Status: operationresult.NotFound, Message: "受管文件目录不存在"}, nil
	}
	if err != nil {
		return operationresult.Result[ReadFileResult]{}, err
	}
	if opened.Status != operationresult.Success {
		return operationresult.Result[ReadFileResult]{Status: opened.Status, Message: opened.Message}, nil
	}
	root := opened.Data
	defer root.Close()
	info, err := root.Stat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return operationresult.Result[ReadFileResult]{Status: operationresult.NotFound, Message: "目标文件不存在"}, nil
	}
	if err != nil {
		return operationresult.Result[ReadFileResult]{}, err
	}
	if !info.Mode().IsRegular() {
		return operationresult.Result[ReadFileResult]{Status: operationresult.InvalidTarget, Message: "目标不是普通文件"}, nil
	}
	var data []byte
	if req.Offset != 0 || req.Limit != 0 {
		file, err := root.Open(name)
		if err != nil {
			return operationresult.Result[ReadFileResult]{}, err
		}
		defer file.Close()
		if _, err := file.Seek(req.Offset, io.SeekStart); err != nil {
			return operationresult.Result[ReadFileResult]{}, err
		}
		var reader io.Reader = file
		if req.Limit != 0 {
			reader = io.LimitReader(file, req.Limit)
		}
		data, err = io.ReadAll(reader)
		if err != nil {
			return operationresult.Result[ReadFileResult]{}, err
		}
	} else {
		data, err = root.ReadFile(name)
		if err != nil {
			return operationresult.Result[ReadFileResult]{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[ReadFileResult]{}, err
	}
	result.Content = string(data)
	result.Revision = fileRevision(data)
	result.Size = info.Size()
	result.Offset = req.Offset
	result.NextOffset = req.Offset + int64(len(data))
	result.Truncated = result.NextOffset < result.Size
	return operationresult.Result[ReadFileResult]{Status: operationresult.Success, Data: result}, nil
}

// WriteFile 原样写入前端提交的 UTF-8 文本，不转换编码、换行或创建撤销记录。
func (m *Supervisor) WriteFile(ctx context.Context, req WriteFileRequest) (operationresult.Result[WriteFileResult], error) {
	result := WriteFileResult{Root: req.Root, Path: req.Path}
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[WriteFileResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[WriteFileResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	if req.Root == "logs" {
		return operationresult.Result[WriteFileResult]{Status: operationresult.Forbidden, Message: "日志文件只读"}, nil
	}
	if !utf8.ValidString(req.Content) {
		return operationresult.Result[WriteFileResult]{Status: operationresult.InvalidArguments, Message: "content 必须是 UTF-8 文本"}, nil
	}
	if req.Create && req.ExpectedRevision != "" {
		return operationresult.Result[WriteFileResult]{Status: operationresult.InvalidArguments, Message: "创建文件不能提供 expected_revision"}, nil
	}
	if !req.Create && req.ExpectedRevision == "" {
		return operationresult.Result[WriteFileResult]{Status: operationresult.InvalidArguments, Message: "保存已有文件必须提供 expected_revision"}, nil
	}
	m.filesMu.Lock()
	defer m.filesMu.Unlock()
	if err := ctx.Err(); err != nil {
		return operationresult.Result[WriteFileResult]{}, err
	}
	opened, name, err := m.openManaged(req.Root, req.Path, req.Create, false)
	if errors.Is(err, fs.ErrNotExist) {
		return operationresult.Result[WriteFileResult]{Status: operationresult.NotFound, Message: "受管文件目录不存在"}, nil
	}
	if err != nil {
		return operationresult.Result[WriteFileResult]{}, err
	}
	if opened.Status != operationresult.Success {
		return operationresult.Result[WriteFileResult]{Status: opened.Status, Message: opened.Message}, nil
	}
	root := opened.Data
	defer root.Close()
	info, err := root.Stat(name)
	created := errors.Is(err, fs.ErrNotExist)
	if err != nil && !created {
		return operationresult.Result[WriteFileResult]{}, err
	}
	if req.Create && !created {
		return operationresult.Result[WriteFileResult]{Status: operationresult.Conflict, Message: "目标文件已存在"}, nil
	}
	if !req.Create && created {
		return operationresult.Result[WriteFileResult]{Status: operationresult.NotFound, Message: "目标文件不存在"}, nil
	}
	if !created {
		if !info.Mode().IsRegular() {
			return operationresult.Result[WriteFileResult]{Status: operationresult.InvalidTarget, Message: "目标不是普通文件"}, nil
		}
		data, err := root.ReadFile(name)
		if err != nil {
			return operationresult.Result[WriteFileResult]{}, err
		}
		if fileRevision(data) != req.ExpectedRevision {
			return operationresult.Result[WriteFileResult]{Status: operationresult.Conflict, Message: "文件已经改变，请重新读取"}, nil
		}
	} else if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return operationresult.Result[WriteFileResult]{}, err
	}
	// 日志只读约束作用于实际目标，也覆盖来自其他逻辑根的路径或符号链接。
	target := filepath.Join(root.Name(), name)
	if created {
		parent, err := filepath.EvalSymlinks(filepath.Dir(target))
		if err != nil {
			return operationresult.Result[WriteFileResult]{}, err
		}
		target = filepath.Join(parent, filepath.Base(target))
	} else {
		target, err = filepath.EvalSymlinks(target)
		if err != nil {
			return operationresult.Result[WriteFileResult]{}, err
		}
	}
	if m.isLogTarget(target) {
		return operationresult.Result[WriteFileResult]{Status: operationresult.Forbidden, Message: "日志文件只读"}, nil
	}
	flags := os.O_WRONLY | os.O_TRUNC
	if created {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	file, err := root.OpenFile(name, flags, 0o600)
	if created && errors.Is(err, fs.ErrExist) {
		return operationresult.Result[WriteFileResult]{Status: operationresult.Conflict, Message: "目标文件已存在"}, nil
	}
	if err != nil {
		return operationresult.Result[WriteFileResult]{}, err
	}
	_, writeErr := io.WriteString(file, req.Content)
	closeErr := file.Close()
	if writeErr != nil {
		return operationresult.Result[WriteFileResult]{}, writeErr
	}
	if closeErr != nil {
		return operationresult.Result[WriteFileResult]{}, closeErr
	}
	result.Created = created
	result.Revision = fileRevision([]byte(req.Content))
	return operationresult.Result[WriteFileResult]{Status: operationresult.Success, Data: result}, nil
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

func managedPath(path string, dir bool) operationresult.Result[string] {
	if path == "" && dir {
		return operationresult.Result[string]{Status: operationresult.Success, Data: "."}
	}
	if strings.ContainsAny(path, "\\\x00") || !filepath.IsLocal(filepath.FromSlash(path)) {
		return operationresult.Result[string]{Status: operationresult.InvalidArguments, Message: "path 必须是受管根内的相对路径"}
	}
	path = filepath.Clean(filepath.FromSlash(path))
	if path == "." && !dir {
		return operationresult.Result[string]{Status: operationresult.InvalidArguments, Message: "必须指定文件 path"}
	}
	if !allowedManagedPath(path) {
		return operationresult.Result[string]{Status: operationresult.InvalidTarget, Message: "该路径不是受管文本文件"}
	}
	return operationresult.Result[string]{Status: operationresult.Success, Data: path}
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
func (m *Supervisor) openManaged(kind, path string, create, dir bool) (operationresult.Result[*os.Root], string, error) {
	checked := managedPath(path, dir)
	if checked.Status != operationresult.Success {
		return operationresult.Result[*os.Root]{Status: checked.Status, Message: checked.Message}, "", nil
	}
	name := checked.Data
	if kind == "config" {
		actual, ok := m.configFiles[name]
		if !ok {
			return operationresult.Result[*os.Root]{Status: operationresult.NotFound, Message: "不是已登记的配置文件"}, "", nil
		}
		root, err := openManagedRoot(filepath.Dir(actual), create)
		if err != nil {
			return operationresult.Result[*os.Root]{}, "", err
		}
		return operationresult.Result[*os.Root]{Status: operationresult.Success, Data: root}, filepath.Base(actual), nil
	}
	base, ok := m.roots[kind]
	if !ok {
		return operationresult.Result[*os.Root]{Status: operationresult.NotFound, Message: "受管文件根不存在"}, "", nil
	}
	if kind == "logs" {
		root, err := os.OpenRoot(base)
		if err != nil {
			return operationresult.Result[*os.Root]{}, "", err
		}
		return operationresult.Result[*os.Root]{Status: operationresult.Success, Data: root}, name, nil
	}
	configDir := m.roots["memory"]
	child := kind
	if kind == "memory" {
		if name == "memories.toml" {
			root, err := os.OpenRoot(configDir)
			if err != nil {
				return operationresult.Result[*os.Root]{}, "", err
			}
			return operationresult.Result[*os.Root]{Status: operationresult.Success, Data: root}, name, nil
		}
		if name != "long_memory" && !strings.HasPrefix(filepath.ToSlash(name), "long_memory/") {
			return operationresult.Result[*os.Root]{Status: operationresult.InvalidTarget, Message: "不是受管记忆文件"}, "", nil
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
	opened, err := openManagedChildRoot(configDir, child, create)
	return opened, name, err
}

// 固定子目录不能通过根位置的符号链接扩大范围。
func openManagedChildRoot(base, child string, create bool) (operationresult.Result[*os.Root], error) {
	parent, err := os.OpenRoot(base)
	if err != nil {
		return operationresult.Result[*os.Root]{}, err
	}
	defer parent.Close()
	info, err := parent.Lstat(child)
	if errors.Is(err, fs.ErrNotExist) && create {
		if err := parent.Mkdir(child, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return operationresult.Result[*os.Root]{}, err
		}
		info, err = parent.Lstat(child)
	}
	if err != nil {
		return operationresult.Result[*os.Root]{}, err
	}
	if !info.IsDir() {
		return operationresult.Result[*os.Root]{Status: operationresult.InvalidTarget, Message: "受管根必须是普通目录"}, nil
	}
	root, err := parent.OpenRoot(child)
	if err != nil {
		return operationresult.Result[*os.Root]{}, err
	}
	opened, err := root.Stat(".")
	if err != nil {
		root.Close()
		return operationresult.Result[*os.Root]{}, err
	}
	if !os.SameFile(info, opened) {
		root.Close()
		return operationresult.Result[*os.Root]{Status: operationresult.Conflict, Message: "受管目录已经改变"}, nil
	}
	return operationresult.Result[*os.Root]{Status: operationresult.Success, Data: root}, nil
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

func (m *Supervisor) fixedFileEntry(ctx context.Context, kind, name string) (operationresult.Result[FileEntry], error) {
	entry := FileEntry{Name: name, Path: name, Kind: "file", Writable: true}
	if name == "long_memory" {
		entry.Kind = "directory"
	}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[FileEntry]{}, err
	}
	opened, path, err := m.openManaged(kind, name, false, true)
	if errors.Is(err, fs.ErrNotExist) {
		return operationresult.Result[FileEntry]{Status: operationresult.Success, Data: entry}, nil
	}
	if err != nil {
		return operationresult.Result[FileEntry]{}, err
	}
	if opened.Status != operationresult.Success {
		return operationresult.Result[FileEntry]{Status: opened.Status, Message: opened.Message}, nil
	}
	root := opened.Data
	defer root.Close()
	info, err := root.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return operationresult.Result[FileEntry]{Status: operationresult.Success, Data: entry}, nil
	}
	if err != nil {
		return operationresult.Result[FileEntry]{}, err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return operationresult.Result[FileEntry]{Status: operationresult.InvalidTarget, Message: "目标不是普通文件"}, nil
	}
	entry.Exists = true
	entry.Size = info.Size()
	if info.IsDir() {
		entry.Kind = "directory"
	}
	return operationresult.Result[FileEntry]{Status: operationresult.Success, Data: entry}, nil
}
