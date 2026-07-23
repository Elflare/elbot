package processenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Environment is an immutable process environment snapshot.
// The zero value preserves os/exec's normal inheritance behavior.
type Environment struct {
	environ    []string
	path       string
	pathExt    string
	configured bool
}

// New builds an environment snapshot from entries in NAME=value form.
func New(entries []string) Environment {
	environment := Environment{
		environ:    make([]string, 0, len(entries)),
		configured: true,
	}
	indexes := make(map[string]int, len(entries))
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		key := environmentKey(name)
		if _, exists := indexes[key]; exists {
			continue
		}
		indexes[key] = len(environment.environ)
		environment.environ = append(environment.environ, name+"="+value)
		environment.setCachedValue(key, value)
	}
	return environment
}

// Fill adds missing values while preserving existing entries. PATH is always
// appended so config files can add executable directories without restating
// the inherited system path.
func (e Environment) Fill(values map[string]string) Environment {
	return e.merge(values, false)
}

// Overlay replaces existing values with values from the new layer. PATH keeps
// append semantics so every layer only needs to declare additional directories.
func (e Environment) Overlay(values map[string]string) Environment {
	return e.merge(values, true)
}

func (e Environment) merge(values map[string]string, replace bool) Environment {
	if !e.configured {
		e = New(os.Environ())
	} else {
		e.environ = append([]string(nil), e.environ...)
	}
	e.configured = true

	indexes := make(map[string]int, len(e.environ))
	current := make(map[string]string, len(e.environ))
	for index, entry := range e.environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		key := environmentKey(name)
		indexes[key] = index
		current[key] = value
	}

	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if strings.TrimSpace(name) == "" {
			continue
		}
		key := environmentKey(name)
		value := values[name]
		if key == environmentKey("PATH") {
			value = mergePathLists(current[key], value)
			if index, exists := indexes[key]; exists {
				originalName, _, _ := strings.Cut(e.environ[index], "=")
				e.environ[index] = originalName + "=" + value
			} else {
				indexes[key] = len(e.environ)
				e.environ = append(e.environ, name+"="+value)
			}
			current[key] = value
			e.setCachedValue(key, value)
			continue
		}
		if index, exists := indexes[key]; exists {
			if !replace {
				continue
			}
			originalName, _, _ := strings.Cut(e.environ[index], "=")
			e.environ[index] = originalName + "=" + value
		} else {
			indexes[key] = len(e.environ)
			e.environ = append(e.environ, name+"="+value)
		}
		current[key] = value
		e.setCachedValue(key, value)
	}
	return e
}

// Configured reports whether the environment is an explicit snapshot.
func (e Environment) Configured() bool {
	return e.configured
}

// Environ returns a copy of the effective environment.
func (e Environment) Environ() []string {
	if !e.configured {
		return os.Environ()
	}
	return append([]string(nil), e.environ...)
}

func (e Environment) Command(name string, args ...string) *exec.Cmd {
	return e.CommandContext(context.Background(), name, args...)
}

func (e Environment) CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	environ := e.Environ()
	resolved := name
	if path, err := exec.LookPath(name); err == nil {
		resolved = path
	} else if path, ok := findExecutable(name, e.path, e.pathExt); ok {
		resolved = path
	}
	cmd := exec.CommandContext(ctx, resolved, args...)
	if resolved != name {
		cmd.Args[0] = name
	}
	cmd.Env = environ
	return cmd
}

func (e *Environment) setCachedValue(key, value string) {
	switch key {
	case environmentKey("PATH"):
		e.path = value
	case environmentKey("PATHEXT"):
		e.pathExt = value
	}
}

func environmentKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

func mergePathLists(primary, extra string) string {
	seen := map[string]struct{}{}
	merged := make([]string, 0)
	for _, list := range []string{primary, extra} {
		for _, dir := range filepath.SplitList(list) {
			if dir == "" {
				continue
			}
			key := dir
			if runtime.GOOS == "windows" {
				key = strings.ToUpper(key)
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, dir)
		}
	}
	return strings.Join(merged, string(os.PathListSeparator))
}

func findExecutable(name, pathValue, pathExt string) (string, bool) {
	if name == "" || filepath.IsAbs(name) || filepath.VolumeName(name) != "" || strings.ContainsAny(name, `/\`) {
		return "", false
	}
	for _, dir := range filepath.SplitList(pathValue) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		base := filepath.Join(dir, name)
		for _, candidate := range executableCandidates(base, pathExt) {
			info, err := os.Stat(candidate)
			if err != nil || info.IsDir() {
				continue
			}
			if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
				continue
			}
			return candidate, true
		}
	}
	return "", false
}

func executableCandidates(path, pathExt string) []string {
	if runtime.GOOS != "windows" {
		return []string{path}
	}
	extensions := filepath.SplitList(pathExt)
	if len(extensions) == 0 {
		extensions = []string{".com", ".exe", ".bat", ".cmd"}
	}
	ext := strings.ToLower(filepath.Ext(path))
	for _, candidateExt := range extensions {
		if ext == strings.ToLower(candidateExt) {
			return []string{path}
		}
	}
	candidates := make([]string, 0, len(extensions))
	for _, candidateExt := range extensions {
		if candidateExt != "" {
			candidates = append(candidates, path+candidateExt)
		}
	}
	return candidates
}
