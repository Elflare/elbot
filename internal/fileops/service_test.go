package fileops_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/fileops"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/testutil"
	"elbot/internal/workspace"
)

type fileFixture struct {
	ctx      context.Context
	sessions *session.Service
	files    *fileops.Service
	binding  *session.Binding
	row      *storage.Session
	scope    session.Scope
	work     *session.WorkspaceStore
	dir      string
	path     string
}

func newFileFixture(t *testing.T) *fileFixture {
	t.Helper()
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{ID: "admin", Role: contextinfo.RoleSuperadmin})
	store := testutil.NewSQLiteStore(t)
	sessions := session.NewService(store)
	scope := session.Scope{ActorID: "admin", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	row, err := sessions.Create(ctx, scope, session.CreateRequest{Title: "files"})
	if err != nil {
		t.Fatal(err)
	}
	_, binding, err := sessions.CurrentBound(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	work := session.NewWorkspaceStore(sessions, store.Sessions(), row.ID)
	dir := t.TempDir()
	if err := work.SetWorkspaceDir(ctx, dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	return &fileFixture{ctx: ctx, sessions: sessions, files: fileops.NewService(nil), binding: binding, row: row, scope: scope, work: work, dir: dir, path: path}
}
func (f *fileFixture) call(ctx context.Context, admission fileops.CommitAdmission) context.Context {
	if admission == nil {
		admission = func(ctx context.Context) (context.Context, func(), error) {
			return f.sessions.EnterBinding(ctx, f.binding)
		}
	}
	ctx = session.WithBinding(ctx, f.binding)
	ctx = workspace.WithWorkspaceStore(ctx, f.work)
	return f.files.WithBinding(ctx, f.binding, admission)
}
func editRequest(path, before, after string) fileops.EditRequest {
	return fileops.EditRequest{Path: path, Edits: []fileops.Edit{{Operation: "replace_text", OldText: before, NewText: &after}}}
}
func readContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("content=%q err=%v want=%q", data, err, want)
	}
}
func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("barrier timed out")
	}
}
func waitError(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("operation timed out")
		return nil
	}
}

func TestEditConfirmationPinsState(t *testing.T) {
	for _, scenario := range []string{"unchanged", "content", "workspace", "absolute", "roundtrip", "parameters", "appeared", "binding", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFileFixture(t)
			ctx := f.call(f.ctx, nil)
			req := editRequest("file", "before", "after")
			if scenario == "absolute" {
				req.Path = f.path
			}
			if scenario == "appeared" {
				if err := os.Remove(f.path); err != nil {
					t.Fatal(err)
				}
				text := "created"
				req = fileops.EditRequest{Path: "file", Create: true, Edits: []fileops.Edit{{Operation: "overwrite", NewText: &text}}}
			}
			if scenario == "symlink" {
				real := filepath.Join(f.dir, "real")
				if err := os.Rename(f.path, real); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(real, f.path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			prepared, err := f.files.PrepareEdit(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			first := prepared.Preview()
			if first.NewBytes != nil {
				t.Fatal("confirmation preview includes file bytes")
			}
			want, refuse := "after", false
			switch scenario {
			case "content":
				// Anchor still matches, but the approved diff no longer describes the file.
				if err := os.WriteFile(f.path, []byte("before changed"), 0600); err != nil {
					t.Fatal(err)
				}
				want, refuse = "before changed", true
			case "workspace", "absolute", "roundtrip":
				other := t.TempDir()
				if err := os.WriteFile(filepath.Join(other, "file"), []byte("before"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := f.work.SetWorkspaceDir(f.ctx, other); err != nil {
					t.Fatal(err)
				}
				if scenario == "roundtrip" {
					if err := f.work.SetWorkspaceDir(f.ctx, f.dir); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "workspace" {
					want, refuse = "before", true
				}
			case "parameters":
				*req.Edits[0].NewText = "different"
			case "appeared":
				if err := os.WriteFile(f.path, nil, 0600); err != nil {
					t.Fatal(err)
				}
				want, refuse = "", true
			case "symlink":
				other := filepath.Join(f.dir, "other")
				if err := os.WriteFile(other, []byte("before"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(f.path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, f.path); err != nil {
					t.Fatal(err)
				}
				want, refuse = "before", true
			case "binding":
				if _, err := f.sessions.Create(f.ctx, f.scope, session.CreateRequest{Title: "next"}); err != nil {
					t.Fatal(err)
				}
				if _, err := f.sessions.Resume(f.ctx, f.scope, f.row.ID); err != nil {
					t.Fatal(err)
				}
				want, refuse = "before", true
			}
			// Derived request contexts cannot refresh a pinned preflight.
			derived := f.files.WithBinding(context.WithValue(ctx, struct{}{}, true), f.binding)
			preview := prepared.Preview()
			if preview.Diff != first.Diff {
				t.Fatal("confirmation diff changed")
			}
			result, _, err := prepared.Commit(derived)
			if (err != nil) != refuse {
				t.Fatalf("edit err=%v refuse=%t", err, refuse)
			}
			if !refuse && (result.DryRun || result.Diff != first.Diff || result.RevisionAfter != first.RevisionAfter || result.RevisionBefore != first.RevisionBefore) {
				t.Fatalf("commit differs from preview: result=%+v preview=%+v", result, first)
			}
			readContent(t, f.path, want)
		})
	}
}

func TestPreparedEditPreservesEncodingAndRollback(t *testing.T) {
	for _, tc := range []struct {
		name, encoding, ending string
		bom                    []byte
		create                 bool
	}{
		{name: "utf8", encoding: "utf-8", ending: "\n"},
		{name: "utf8-bom-crlf", encoding: "utf-8-bom", ending: "\r\n", bom: []byte{0xEF, 0xBB, 0xBF}},
		{name: "utf16-crlf", encoding: "utf-16le", ending: "\r\n", bom: []byte{0xFF, 0xFE}},
		{name: "create-bom", encoding: "utf-8-bom", ending: "\n", bom: []byte{0xEF, 0xBB, 0xBF}, create: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFileFixture(t)
			ctx := f.call(f.ctx, nil)
			original, err := fileops.EncodeText("before"+tc.ending, tc.encoding, tc.bom)
			if err != nil {
				t.Fatal(err)
			}
			req := editRequest("file", "before", "after")
			req.Encoding = tc.encoding
			if tc.create {
				if err := os.Remove(f.path); err != nil {
					t.Fatal(err)
				}
				req.Create = true
				text := "after\n"
				req.Edits = []fileops.Edit{{Operation: "overwrite", NewText: &text}}
			} else if err := os.WriteFile(f.path, original, 0600); err != nil {
				t.Fatal(err)
			}
			prepared, err := f.files.PrepareEdit(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			preview := prepared.Preview()
			result, _, err := prepared.Commit(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want, err := fileops.EncodeText("after"+tc.ending, tc.encoding, tc.bom)
			if err != nil {
				t.Fatal(err)
			}
			written, err := os.ReadFile(f.path)
			if err != nil || !bytes.Equal(written, want) || !bytes.Equal(result.NewBytes, want) {
				t.Fatalf("written=%x result=%x want=%x err=%v", written, result.NewBytes, want, err)
			}
			if preview.RevisionAfter != fileops.ContentRevision(written) || preview.Diff != result.Diff || !result.RollbackAvailable || result.Created != tc.create {
				t.Fatalf("commit=%+v preview=%+v", result, preview)
			}
			if _, err := f.files.Rollback(ctx, "file", 0); err != nil {
				t.Fatal(err)
			}
			if tc.create {
				if _, err := os.Stat(f.path); !os.IsNotExist(err) {
					t.Fatalf("created file survived rollback: %v", err)
				}
			} else {
				restored, err := os.ReadFile(f.path)
				if err != nil || !bytes.Equal(restored, original) {
					t.Fatalf("restored=%x want=%x err=%v", restored, original, err)
				}
				info, err := os.Stat(f.path)
				if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
					t.Fatalf("original mode not restored: %v %v", info, err)
				}
			}
		})
	}
}

func TestFileCommitSerializesSessionTransitions(t *testing.T) {
	for _, operation := range []string{"edit", "prepared", "restore", "remove"} {
		for _, transition := range []string{"switch", "delete", "stop"} {
			for _, commitFirst := range []bool{false, true} {
				name := operation + "/" + transition + "/transition_first"
				if commitFirst {
					name = operation + "/" + transition + "/commit_first"
				}
				t.Run(name, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						f := newFileFixture(t)
						if operation != "edit" && operation != "prepared" {
							if operation == "remove" {
								if err := os.Remove(f.path); err != nil {
									t.Fatal(err)
								}
							}
							req := editRequest("file", "before", "after")
							if operation == "remove" {
								text := "after"
								req = fileops.EditRequest{Path: "file", Create: true, Edits: []fileops.Edit{{Operation: "overwrite", NewText: &text}}}
							}
							if _, _, err := f.files.Edit(f.call(f.ctx, nil), req); err != nil {
								t.Fatal(err)
							}
						}
						base, cancel := context.WithCancel(f.ctx)
						defer cancel()
						reached, allow := make(chan struct{}), make(chan struct{})
						admission := func(ctx context.Context) (context.Context, func(), error) {
							if !commitFirst {
								close(reached)
								<-allow
							}
							locked, release, err := f.sessions.EnterBinding(ctx, f.binding)
							if err != nil {
								return ctx, nil, err
							}
							if commitFirst {
								close(reached)
								<-allow
							}
							return locked, release, nil
						}
						ctx := f.call(base, admission)
						done := make(chan error, 1)
						go func() {
							if operation == "prepared" {
								prepared, err := f.files.PrepareEdit(ctx, editRequest("file", "before", "after"))
								if err == nil {
									_, _, err = prepared.Commit(ctx)
								}
								done <- err
							} else if operation == "edit" {
								_, _, err := f.files.Edit(ctx, editRequest("file", "before", "after"))
								done <- err
							} else {
								_, err := f.files.Rollback(ctx, "file", 0)
								done <- err
							}
						}()
						waitSignal(t, reached)
						if operation != "remove" {
							staged, _ := filepath.Glob(filepath.Join(f.dir, ".file.tmp-*"))
							if len(staged) != 1 {
								t.Fatalf("temporary output not prepared before admission: %v", staged)
							}
						}
						transitionCall := func(ctx context.Context) error {
							switch transition {
							case "switch":
								_, err := f.sessions.Create(ctx, f.scope, session.CreateRequest{Title: "next"})
								return err
							case "delete":
								return f.sessions.Delete(ctx, f.scope, f.row.ID)
							default:
								_, release, err := f.sessions.EnterSessions(ctx, f.row.ID)
								if err != nil {
									return err
								}
								defer release()
								cancel()
								return nil
							}
						}
						if commitFirst {
							deadline, stop := context.WithTimeout(f.ctx, 20*time.Millisecond)
							err := transitionCall(deadline)
							stop()
							if !errors.Is(err, context.DeadlineExceeded) {
								t.Fatalf("transition passed active commit: %v", err)
							}
						} else if err := transitionCall(f.ctx); err != nil {
							t.Fatal(err)
						}
						close(allow)
						err := waitError(t, done)
						if (err == nil) != commitFirst {
							t.Fatalf("commit first=%t error=%v", commitFirst, err)
						}
						if commitFirst {
							if err := transitionCall(f.ctx); err != nil {
								t.Fatal(err)
							}
						}
						want := "before"
						if operation != "edit" && operation != "prepared" {
							want = "after"
						}
						if commitFirst {
							if operation == "remove" {
								if _, err := os.Stat(f.path); !errors.Is(err, os.ErrNotExist) {
									t.Fatalf("remove=%v", err)
								}
							} else {
								if operation == "edit" || operation == "prepared" {
									want = "after"
								} else {
									want = "before"
								}
								readContent(t, f.path, want)
							}
						} else {
							readContent(t, f.path, want)
						}
						staged, _ := filepath.Glob(filepath.Join(f.dir, ".file.tmp-*"))
						if len(staged) != 0 {
							t.Fatalf("temporary output leaked: %v", staged)
						}
					})
				})
			}
		}
	}
}

func TestBackgroundEditUsesSharedTargetLockAndCancellation(t *testing.T) {
	f := newFileFixture(t)
	reached, allow := make(chan struct{}), make(chan struct{})
	ctx := f.call(f.ctx, func(ctx context.Context) (context.Context, func(), error) {
		locked, release, err := f.sessions.EnterBinding(ctx, f.binding)
		if err != nil {
			return ctx, nil, err
		}
		close(reached)
		<-allow
		return locked, release, nil
	})
	first := make(chan error, 1)
	go func() { _, _, err := f.files.Edit(ctx, editRequest("file", "before", "after")); first <- err }()
	waitSignal(t, reached)
	bg, cancel := context.WithCancel(sandboxctx.WithSandboxContext(f.ctx, sandboxctx.SandboxContext{Background: true, Dir: f.dir}))
	bg = f.files.WithBinding(bg, nil)
	waiting := make(chan error, 1)
	go func() { _, _, err := f.files.Edit(bg, editRequest("file", "before", "other")); waiting <- err }()
	cancel()
	if err := waitError(t, waiting); !errors.Is(err, context.Canceled) {
		t.Fatalf("background wait=%v", err)
	}
	close(allow)
	if err := waitError(t, first); err != nil {
		t.Fatal(err)
	}
	readContent(t, f.path, "after")
	bg = sandboxctx.WithSandboxContext(f.ctx, sandboxctx.SandboxContext{Background: true, Dir: f.dir})
	result, _, err := f.files.Edit(f.files.WithBinding(bg, nil), editRequest("file", "after", "background"))
	if err != nil || result.RollbackAvailable {
		t.Fatalf("background result=%+v err=%v", result, err)
	}
	if _, err := f.files.Rollback(f.call(f.ctx, nil), "file", 0); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale foreground backup=%v", err)
	}
}
