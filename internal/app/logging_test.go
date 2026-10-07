package app

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/logging"
	"elbot/internal/notification"
)

func TestProductionGlobalLogsKeepUsageAndBoundBodies(t *testing.T) {
	for _, level := range []string{"info", "debug", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			req, _, model := runtimeAssemblyFixtureWithLogLevel(t, level)
			model.chunks = [][]chatcompletions.Chunk{{{DeltaContent: "assembled answer", Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}}}
			runtime, err := (defaultRuntimeFactory{}).Build(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeAssembledRuntime(t, runtime) })
			body := strings.Repeat("正文", 300) + "end-marker"
			if err := runtime.Agent.HandleMessage(context.Background(), body); err != nil {
				t.Fatal(err)
			}
			notices := notification.New(runtime.Dispatcher, true)
			notices.Text(context.Background(), slog.LevelWarn, "background notice")
			manager := hook.NewManager()
			if err := manager.Register(hook.Registration{Point: hook.PointErrorOccurred, Name: "audit-hook", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, error) {
				return event, errors.New("hook failure")
			})}); err != nil {
				t.Fatal(err)
			}
			_ = manager.Notify(context.Background(), hook.Event{Point: hook.PointErrorOccurred})
			profiler := newStartupProfiler(time.Now())
			profiler.SetEnabled(true)
			profiler.Mark("test-stage")
			profiler.Flush()
			if err := req.Foundation.Logs.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			reader := logging.Reader{Dir: req.Foundation.Logs.LogDir()}
			audit, err := reader.Query(context.Background(), logging.LogQuery{Prefix: "audit", Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, entry := range audit {
				if entry.Fields["event"] == "llm_usage" {
					found = true
					if entry.Fields["total_tokens"] == "" {
						t.Fatal("lost usage fields")
					}
				}
			}
			if !found {
				t.Fatal("runtime level suppressed llm_usage")
			}
			entries, err := reader.Query(context.Background(), logging.LogQuery{Prefix: "elbot", Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, entry := range entries {
				counts[entry.Fields["event"]]++
				if entry.Fields["event"] == "hook_error" && entry.Level != "ERROR" {
					t.Fatalf("final Hook failure severity: %+v", entry)
				}
				if entry.Fields["event"] == "user_message" {
					if strings.Contains(entry.Raw, "end-marker") != (level == "debug") {
						t.Fatalf("body policy for %s: %s", level, entry.Raw)
					}
					if strings.Contains(entry.Fields["msg"], "end-marker") {
						t.Fatal("full body in summary")
					}
				}
			}
			if level == "info" || level == "debug" {
				if counts["user_message"] != 1 || counts["assistant_message"] != 1 {
					t.Fatal(counts)
				}
			} else if counts["user_message"] != 0 {
				t.Fatal(counts)
			}
			if counts["hook_error"] != 1 || (level != "error" && counts["runtime_notification"] != 1) {
				t.Fatal(counts)
			}
			if (counts["startup_stage"] == 1) != (level == "debug") {
				t.Fatal(counts)
			}
		})
	}
}

func TestBusinessLoggingDependencyBoundaries(t *testing.T) {
	root := ".."
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		aliases := map[string]string{}
		for _, imp := range file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			name := filepath.Base(p)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			aliases[name] = p
		}
		infrastructure := strings.HasPrefix(rel, "logging/") || strings.HasPrefix(rel, "signal/")
		ast.Inspect(file, func(node ast.Node) bool {
			if selector, ok := node.(*ast.SelectorExpr); ok {
				if id, ok := selector.X.(*ast.Ident); ok && aliases[id.Name] == "log/slog" && !infrastructure {
					switch selector.Sel.Name {
					case "Logger", "Handler", "New", "NewTextHandler", "NewJSONHandler", "Default", "SetDefault", "Info", "Warn", "Error", "Debug", "InfoContext", "WarnContext", "ErrorContext", "DebugContext":
						t.Errorf("business logger bypass: %s: slog.%s", rel, selector.Sel.Name)
					}
				}
			}
			if call, ok := node.(*ast.CallExpr); ok {
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Emit" {
					if source, ok := selector.X.(*ast.SelectorExpr); ok && source.Sel.Name == "LogSubmitted" {
						t.Errorf("snapshot bypass: %s", rel)
					}
				}
			}
			return true
		})
		if strings.HasPrefix(rel, "logging/") || strings.HasPrefix(rel, "events/") || rel == "agent/logging.go" || rel == "session/naming_logging.go" || rel == "modelmgr/logging.go" {
			for _, p := range aliases {
				if strings.HasPrefix(p, "elbot/internal/llm/") {
					t.Errorf("protocol dependency in log consumer: %s", rel)
				}
			}
		}
		if rel == "events/logging.go" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, name := range []string{"SetLogger(", "auditFunc(", "writeAudit("} {
			if strings.Contains(string(data), name) {
				t.Errorf("retired log injection: %s: %s", rel, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
