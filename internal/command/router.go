package command

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"elbot/internal/contextinfo"
)

type Router struct {
	prefixes    []string
	handlers    map[string]Handler
	primary     map[string]bool
	order       []string
	commandText func(string, ...string) string // 绑定配置首个前缀，不受解析排序影响。
}

// NewRouter 接收 config 已规范化且非空的前缀；补全使用独立的最长优先副本。
func NewRouter(prefixes []string) *Router {
	prefix := prefixes[0]
	prefixes = append([]string(nil), prefixes...)
	sort.SliceStable(prefixes, func(i, j int) bool { return len(prefixes[i]) > len(prefixes[j]) })
	return &Router{
		commandText: func(name string, args ...string) string { return Format(prefix, name, args...) },
		prefixes:    prefixes,
		handlers:    map[string]Handler{},
		primary:     map[string]bool{},
	}
}

func (r *Router) Register(h Handler) error {
	info := h.Info()
	name := normalizeName(info.Name)
	if name == "" {
		return fmt.Errorf("command name is empty")
	}
	if _, exists := r.handlers[name]; exists {
		return fmt.Errorf("command %q already registered", name)
	}

	names := []string{name}
	for _, alias := range info.Aliases {
		alias = normalizeName(alias)
		if alias == "" {
			continue
		}
		names = append(names, alias)
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			return fmt.Errorf("command %q has duplicate alias %q", name, n)
		}
		seen[n] = true
		if _, exists := r.handlers[n]; exists {
			return fmt.Errorf("command alias %q already registered", n)
		}
	}

	for _, n := range names {
		r.handlers[n] = h
	}
	r.primary[name] = true
	r.order = append(r.order, name)
	return nil
}

func RegisterHandlers(registrar interface{ Register(Handler) error }, handlers ...Handler) error {
	for _, h := range handlers {
		if err := registrar.Register(h); err != nil {
			return err
		}
	}
	return nil
}

func (r *Router) Dispatch(ctx context.Context, raw string) (*Result, error) {
	parsed := r.Parse(raw)
	if !parsed.OK {
		return nil, fmt.Errorf("not a command")
	}
	h, ok := r.handlers[parsed.Name]
	if !ok {
		return &Result{Content: fmt.Sprintf("unk command: %s%s\n%s", parsed.Prefix, parsed.Name, r.commandText("help"))}, nil
	}
	return h.Handle(ctx, Request{Raw: raw, Prefix: parsed.Prefix, Name: parsed.Name, Args: parsed.Args})
}

func (r *Router) IsCommand(text string) bool {
	return r.Parse(text).OK
}

func (r *Router) Parse(text string) Parsed {
	return Parse(text, r.prefixes)
}

func (r *Router) CommandInfo(name string) (Info, bool) {
	name = normalizeName(name)
	h, ok := r.handlers[name]
	if !ok {
		return Info{}, false
	}
	info := h.Info()
	info.Name = normalizeName(info.Name)
	return info, true
}

func (r *Router) CommandInfoForActor(name string, actor contextinfo.Actor) (Info, bool) {
	info, ok := r.CommandInfo(name)
	if !ok || !CanAccess(info, actor) {
		return Info{}, false
	}
	return info, true
}
func (r *Router) Handler(name string) (Handler, bool) {
	h, ok := r.handlers[normalizeName(name)]
	return h, ok
}

func (r *Router) Commands() []Info {
	infos := make([]Info, 0, len(r.order))
	for _, name := range r.order {
		h := r.handlers[name]
		info := h.Info()
		info.Name = normalizeName(info.Name)
		infos = append(infos, info)
	}
	return infos
}

func (r *Router) CommandsForActor(actor contextinfo.Actor) []Info {
	infos := r.Commands()
	out := make([]Info, 0, len(infos))
	for _, info := range infos {
		if CanAccess(info, actor) {
			out = append(out, info)
		}
	}
	return out
}

// Complete returns full-line command completions for the current input.
func (r *Router) Complete(text string) []string {
	return r.complete(text, nil)
}

func (r *Router) CompleteForActor(text string, actor contextinfo.Actor) []string {
	return r.complete(text, &actor)
}

func (r *Router) complete(text string, actor *contextinfo.Actor) []string {
	for _, prefix := range r.prefixes {
		if !strings.HasPrefix(text, prefix) {
			continue
		}

		rest := strings.TrimPrefix(text, prefix)
		if strings.ContainsAny(rest, " \t") {
			return nil
		}

		query := normalizeName(rest)
		out := []string{}
		for _, name := range r.completionNames(actor) {
			if strings.HasPrefix(name, query) {
				out = append(out, prefix+name)
			}
		}
		return out
	}
	return nil
}

func (r *Router) completionNames(actor *contextinfo.Actor) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, primaryName := range r.order {
		info := r.handlers[primaryName].Info()
		if actor != nil && !CanAccess(info, *actor) {
			continue
		}
		names := append([]string{info.Name}, info.Aliases...)
		for _, name := range names {
			name = normalizeName(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func (r *Router) Prefixes() []string {
	out := append([]string(nil), r.prefixes...)
	return out
}

func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
