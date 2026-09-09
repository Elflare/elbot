package tool

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"elbot/internal/media"
	"elbot/internal/storage"
)

// MediaInput is an explicit input declaration, never inferred from command text.
type MediaInput struct {
	MediaID string `json:"media"`
}

func (i *MediaInput) UnmarshalJSON(data []byte) error {
	type input MediaInput
	var value input
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if !media.ValidID(value.MediaID) {
		return fmt.Errorf("invalid media")
	}
	*i = MediaInput(value)
	return nil
}

// MediaRuntime supplies host-owned media access to executable tools.
type MediaRuntime struct {
	Center      *media.Manager
	SandboxRoot string
}

// MediaCall owns references and optional temporary files for one invocation.
type MediaCall struct {
	runtime *MediaRuntime
	owner   string
	refs    map[string]storage.MediaReference
	dir     string
}

func (r *MediaRuntime) NewCall() *MediaCall {
	return &MediaCall{runtime: r, owner: rand.Text(), refs: map[string]storage.MediaReference{}}
}

func (c *MediaCall) Retain(ctx context.Context, id string) (*storage.Media, error) {
	if c.runtime == nil || c.runtime.Center == nil {
		return nil, fmt.Errorf("media center is not configured")
	}
	if !media.ValidID(id) {
		return nil, fmt.Errorf("invalid media")
	}
	metadata, err := c.runtime.Center.Store.Media().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, ok := c.refs[id]; !ok {
		ref := storage.MediaReference{MediaID: id, OwnerType: "skill", OwnerID: c.owner, Purpose: "temporary"}
		if err := c.runtime.Center.AddReference(ctx, &ref); err != nil {
			return nil, err
		}
		c.refs[id] = ref
	}
	return metadata, nil
}

func (c *MediaCall) Close() error {
	var errs []error
	if c.dir != "" {
		errs = append(errs, os.RemoveAll(c.dir))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for id, ref := range c.refs {
		if err := c.runtime.Center.RemoveReference(ctx, ref); err != nil {
			errs = append(errs, err)
		} else {
			delete(c.refs, id)
		}
	}
	return errors.Join(errs...)
}

// Workspace is relative to the process cwd, keeping existing Skill script paths valid.
func (c *MediaCall) Workspace(base string) (string, error) {
	if c.dir == "" {
		dir, err := os.MkdirTemp(base, ".media-call-*")
		if err != nil {
			return "", err
		}
		c.dir = dir
	}
	return filepath.Rel(base, c.dir)
}

func mediaFilename(metadata *storage.Media) string {
	ext := filepath.Ext(metadata.Name)
	for _, ch := range strings.TrimPrefix(ext, ".") {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9') {
			ext = ""
			break
		}
	}
	return strings.TrimPrefix(metadata.ID, media.IDPrefix) + ext
}

// Export writes a private invocation input and returns a path relative to cwd.
func (c *MediaCall) Export(ctx context.Context, id, base string) (string, *storage.Media, error) {
	metadata, err := c.Retain(ctx, id)
	if err != nil {
		return "", nil, err
	}
	relative, err := c.Workspace(base)
	if err != nil {
		return "", nil, err
	}
	name := mediaFilename(metadata)
	root, err := os.OpenRoot(c.dir)
	if err != nil {
		return "", nil, err
	}
	defer root.Close()
	if err := c.write(ctx, root, name, id); err != nil {
		return "", nil, err
	}
	return filepath.Join(relative, name), metadata, nil
}

// CachedExport refreshes ModTime even when a previously exported input is reused.
// The ordinary sandbox retention job owns deletion; there are no active-file locks.
func (c *MediaCall) CachedExport(ctx context.Context, id string) (string, error) {
	metadata, err := c.Retain(ctx, id)
	if err != nil {
		return "", err
	}
	base, err := filepath.Abs(c.runtime.SandboxRoot)
	if err != nil {
		return "", err
	}
	if c.runtime.SandboxRoot == "" {
		return "", fmt.Errorf("media sandbox is not configured")
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := root.MkdirAll("media-inputs", 0700); err != nil {
		return "", err
	}
	name := filepath.Join("media-inputs", mediaFilename(metadata))
	info, err := root.Lstat(name)
	if err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("media cache is not a regular file")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	} else if err := c.write(ctx, root, name, id); err != nil {
		return "", err
	}
	now := time.Now()
	if err := root.Chtimes(name, now, now); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join(base, name)), nil
}

// Import opens a regular output within the Skill cwd, including safe symlink resolution.
func (c *MediaCall) Import(ctx context.Context, base, path string, input media.Input) (*storage.Media, error) {
	if c.runtime == nil || c.runtime.Center == nil {
		return nil, fmt.Errorf("media center is not configured")
	}
	slash := strings.ReplaceAll(path, "\\", "/")
	if !filepath.IsLocal(path) || strings.Contains(slash, ":") || strings.HasPrefix(slash, "/") {
		return nil, fmt.Errorf("skill media path must be relative")
	}
	for _, part := range strings.Split(slash, "/") {
		if part == ".." {
			return nil, fmt.Errorf("skill media path must not contain parent traversal")
		}
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open skill media: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("skill media must be a regular file")
	}
	if input.Name == "" {
		input.Name = filepath.Base(path)
	}
	return c.runtime.Center.ImportReader(ctx, file, info.Size(), input)
}

func (c *MediaCall) write(ctx context.Context, root *os.Root, name, id string) error {
	reader, _, err := c.runtime.Center.Open(ctx, id)
	if err != nil {
		return err
	}
	defer reader.Close()
	temporary := name + "." + rand.Text() + ".tmp"
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	_, copyErr := io.Copy(file, reader)
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return err
	}
	return root.Rename(temporary, name)
}
