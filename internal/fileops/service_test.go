package fileops_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/fileops"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/testutil"
	"elbot/internal/workspace"
)

type fileFixture struct {
	ctx      context.Context
	sessions *session.Supervisor
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
	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin, ActorIdentity: contextinfo.ActorIdentity{Platform: "cli", ID: "admin"}})
	store := testutil.NewSQLiteStore(t)
	sessions := newSessionSupervisor(t, store)
	scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "admin"}, Platform: "cli", ChatID: "local", ChatType: "private"}
	row, err := sessions.Create(ctx, scope, session.CreateRequest{Title: "files"})
	if err != nil || row.Status != operationresult.Success {
		t.Fatalf("create session: %+v %v", row, err)
	}
	current, err := sessions.CurrentBound(ctx, scope)
	if err != nil || current.Status != operationresult.Success {
		t.Fatalf("current binding: %+v %v", current, err)
	}
	work := sessions.WorkspaceStore(row.Data.ID)
	dir := t.TempDir()
	if err := work.SetWorkspaceDir(ctx, dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	return &fileFixture{ctx: ctx, sessions: sessions, files: fileops.NewService(nil), binding: current.Data.Binding, row: row.Data, scope: scope, work: work, dir: dir, path: path}
}
func (f *fileFixture) admit(ctx context.Context) (operationresult.Result[fileops.CommitAdmissionData], error) {
	locked, release, err := f.sessions.EnterBinding(ctx, f.binding)
	if err != nil {
		return operationresult.Result[fileops.CommitAdmissionData]{}, err
	}
	return operationresult.Result[fileops.CommitAdmissionData]{Status: operationresult.Success, Data: fileops.CommitAdmissionData{Context: locked, Release: release}}, nil
}
func (f *fileFixture) call(ctx context.Context, admission fileops.CommitAdmission) context.Context {
	if admission == nil {
		admission = f.admit
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

type fileOutcome struct {
	status operationresult.Status
	err    error
}

func waitOutcome(t *testing.T, ch <-chan fileOutcome) fileOutcome {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("operation timed out")
		return fileOutcome{}
	}
}

func TestEditRejectsBindingExpiredBeforeCaptureAtCommit(t *testing.T) {
	f := newFileFixture(t)
	if result, err := f.sessions.Create(f.ctx, f.scope, session.CreateRequest{Title: "next"}); err != nil || result.Status != operationresult.Success {
		t.Fatalf("switch: %+v %v", result, err)
	}
	entered := false
	ctx := f.call(workspace.WithPathScope(f.ctx, workspace.PathScope{Dir: f.dir, Restricted: true}), func(ctx context.Context) (operationresult.Result[fileops.CommitAdmissionData], error) {
		entered = true
		return f.admit(ctx)
	})
	if _, _, err := f.files.Edit(ctx, editRequest("file", "before", "after")); err == nil || !entered {
		t.Fatalf("expired binding: err=%v entered=%t", err, entered)
	}
	readContent(t, f.path, "before")
}

func TestEditConfirmationPinsState(t *testing.T) {
	for _, scenario := range []string{"unchanged", "content", "workspace", "absolute", "roundtrip", "parameters", "appeared", "binding", "symlink", "policy"} {
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
			if err != nil || prepared.Status != operationresult.Success {
				t.Fatalf("prepare: %+v %v", prepared, err)
			}
			first := prepared.Data.Preview()
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
			case "policy":
				f.files.CheckWrite = func(string) operationresult.Result[struct{}] {
					return operationresult.Result[struct{}]{Status: operationresult.Forbidden, Message: "write denied"}
				}
				want, refuse = "before", true
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
				if result, err := f.sessions.Create(f.ctx, f.scope, session.CreateRequest{Title: "next"}); err != nil || result.Status != operationresult.Success {
					t.Fatalf("switch: %+v %v", result, err)
				}
				if result, err := f.sessions.Resume(f.ctx, f.scope, f.row.ID); err != nil || result.Status != operationresult.Success {
					t.Fatalf("resume: %+v %v", result, err)
				}
				want, refuse = "before", true
			}
			// Derived request contexts cannot refresh a pinned preflight.
			derived := f.files.WithBinding(context.WithValue(ctx, struct{}{}, true), f.binding)
			preview := prepared.Data.Preview()
			if preview.Diff != first.Diff {
				t.Fatal("confirmation diff changed")
			}
			result, _, err := prepared.Data.Commit(derived)
			if err != nil || (result.Status != operationresult.Success) != refuse || refuse && result.Message == "" {
				t.Fatalf("edit=%+v err=%v refuse=%t", result, err, refuse)
			}
			if !refuse && (result.Data.DryRun || result.Data.Diff != first.Diff || result.Data.RevisionAfter != first.RevisionAfter || result.Data.RevisionBefore != first.RevisionBefore) {
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
			if err != nil || prepared.Status != operationresult.Success {
				t.Fatalf("prepare: %+v %v", prepared, err)
			}
			preview := prepared.Data.Preview()
			committed, _, err := prepared.Data.Commit(ctx)
			if err != nil || committed.Status != operationresult.Success {
				t.Fatalf("commit: %+v %v", committed, err)
			}
			result := committed.Data
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
			if result, err := f.files.Rollback(ctx, "file", 0); err != nil || result.Status != operationresult.Success {
				t.Fatalf("rollback: %+v %v", result, err)
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
							if result, _, err := f.files.Edit(f.call(f.ctx, nil), req); err != nil || result.Status != operationresult.Success {
								t.Fatalf("edit: %+v %v", result, err)
							}
						}
						base, cancel := context.WithCancel(f.ctx)
						defer cancel()
						reached, allow := make(chan struct{}), make(chan struct{})
						admission := func(ctx context.Context) (operationresult.Result[fileops.CommitAdmissionData], error) {
							if !commitFirst {
								close(reached)
								<-allow
							}
							admitted, err := f.admit(ctx)
							if err != nil {
								return admitted, err
							}
							if commitFirst {
								close(reached)
								<-allow
							}
							return admitted, nil
						}
						ctx := f.call(base, admission)
						done := make(chan fileOutcome, 1)
						go func() {
							if operation == "prepared" {
								prepared, err := f.files.PrepareEdit(ctx, editRequest("file", "before", "after"))
								if err != nil || prepared.Status != operationresult.Success {
									done <- fileOutcome{prepared.Status, err}
									return
								}
								result, _, err := prepared.Data.Commit(ctx)
								done <- fileOutcome{result.Status, err}
							} else if operation == "edit" {
								result, _, err := f.files.Edit(ctx, editRequest("file", "before", "after"))
								done <- fileOutcome{result.Status, err}
							} else {
								result, err := f.files.Rollback(ctx, "file", 0)
								done <- fileOutcome{result.Status, err}
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
								result, err := f.sessions.Create(ctx, f.scope, session.CreateRequest{Title: "next"})
								if err == nil && result.Status != operationresult.Success {
									t.Fatalf("switch: %+v", result)
								}
								return err
							case "delete":
								result, err := f.sessions.Delete(ctx, f.scope, f.row.ID)
								if err == nil && result.Status != operationresult.Success {
									t.Fatalf("delete: %+v", result)
								}
								return err
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
						result := waitOutcome(t, done)
						if (result.err == nil && result.status == operationresult.Success) != commitFirst {
							t.Fatalf("commit first=%t result=%+v", commitFirst, result)
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
	ctx := f.call(f.ctx, func(ctx context.Context) (operationresult.Result[fileops.CommitAdmissionData], error) {
		admitted, err := f.admit(ctx)
		if err != nil {
			return admitted, err
		}
		close(reached)
		<-allow
		return admitted, nil
	})
	first := make(chan fileOutcome, 1)
	go func() {
		result, _, err := f.files.Edit(ctx, editRequest("file", "before", "after"))
		first <- fileOutcome{result.Status, err}
	}()
	waitSignal(t, reached)
	bg, cancel := context.WithCancel(workspace.WithPathScope(testutil.TaskContext(f.ctx, ""), workspace.PathScope{Dir: f.dir, Restricted: true}))
	bg = f.files.WithBinding(bg, nil)
	waiting := make(chan fileOutcome, 1)
	go func() {
		result, _, err := f.files.Edit(bg, editRequest("file", "before", "other"))
		waiting <- fileOutcome{result.Status, err}
	}()
	cancel()
	if result := waitOutcome(t, waiting); !errors.Is(result.err, context.Canceled) {
		t.Fatalf("background wait=%+v", result)
	}
	close(allow)
	if result := waitOutcome(t, first); result.err != nil || result.status != operationresult.Success {
		t.Fatalf("edit: %+v", result)
	}
	readContent(t, f.path, "after")
	bg = workspace.WithPathScope(testutil.TaskContext(f.ctx, ""), workspace.PathScope{Dir: f.dir, Restricted: true})
	result, _, err := f.files.Edit(f.files.WithBinding(bg, nil), editRequest("file", "after", "background"))
	if err != nil || result.Status != operationresult.Success || result.Data.RollbackAvailable {
		t.Fatalf("background result=%+v err=%v", result, err)
	}
	if result, err := f.files.Rollback(f.call(f.ctx, nil), "file", 0); err != nil || result.Status != operationresult.Conflict || result.Message == "" {
		t.Fatalf("stale foreground backup=%+v %v", result, err)
	}
}
