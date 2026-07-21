package hook

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// ProcessEnvironment is the environment inherited by process Hooks. The
// zero value preserves os/exec's normal inheritance behavior.
type ProcessEnvironment struct {
	environ []string
	path    string
	pathExt string
}

// NewProcessEnvironment merges config .env values into the ElBot process
// environment. Process values win, except that .env PATH entries are appended.
func NewProcessEnvironment(base []string, dotenv map[string]string) ProcessEnvironment {
	entries := append([]string(nil), base...)
	indexes := make(map[string]int, len(entries))
	values := make(map[string]string, len(entries))
	for index, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		key := environmentKey(name)
		if _, exists := indexes[key]; exists {
			continue
		}
		indexes[key] = index
		values[key] = value
	}

	dotenvKeys := make([]string, 0, len(dotenv))
	for name := range dotenv {
		dotenvKeys = append(dotenvKeys, name)
	}
	sort.Strings(dotenvKeys)
	for _, name := range dotenvKeys {
		if strings.TrimSpace(name) == "" {
			continue
		}
		key := environmentKey(name)
		value := dotenv[name]
		if key == environmentKey("PATH") {
			merged := mergePathLists(values[key], value)
			if index, exists := indexes[key]; exists {
				originalName, _, _ := strings.Cut(entries[index], "=")
				entries[index] = originalName + "=" + merged
			} else {
				indexes[key] = len(entries)
				entries = append(entries, name+"="+merged)
			}
			values[key] = merged
			continue
		}
		if _, exists := indexes[key]; exists {
			continue
		}
		indexes[key] = len(entries)
		values[key] = value
		entries = append(entries, name+"="+value)
	}

	return ProcessEnvironment{
		environ: entries,
		path:    values[environmentKey("PATH")],
		pathExt: values[environmentKey("PATHEXT")],
	}
}

func (e ProcessEnvironment) Command(name string, args ...string) *exec.Cmd {
	return e.CommandContext(context.Background(), name, args...)
}

func (e ProcessEnvironment) CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	environ := e.environ
	if environ == nil {
		environ = os.Environ()
	}
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
	cmd.Env = append([]string(nil), environ...)
	return cmd
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
