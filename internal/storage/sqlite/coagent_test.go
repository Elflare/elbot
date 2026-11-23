package sqlite

import (
	"errors"
	"strconv"
	"sync"
	"testing"

	"elbot/internal/storage"
)

func TestCoagentLimitIsAtomicAndPerBoard(t *testing.T) {
	ctx := t.Context()
	db := newTestStore(t)
	repo := db.Coagents()
	build := func(prefix string, count int) []*storage.Session {
		var rows []*storage.Session
		for i := range count {
			name := prefix + strconv.Itoa(i)
			rows = append(rows, &storage.Session{ID: name, Title: name})
		}
		return rows
	}
	for _, main := range []string{"one", "two"} {
		if err := db.Sessions().Create(ctx, &storage.Session{ID: main}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.AddMembers(ctx, "one", "初始", "", build("a", 7)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AddMembers(ctx, "one", "超额", "", build("b", 2)); !errors.Is(err, storage.ErrCoagentLimit) {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, prefix := range []string{"c", "d"} {
		go func() {
			_, err := repo.AddMembers(ctx, "one", "满员", "", build(prefix, 1))
			results <- err
		}()
	}
	winners := 0
	for range 2 {
		err := <-results
		if err == nil {
			winners++
		} else if !errors.Is(err, storage.ErrCoagentLimit) {
			t.Fatal(err)
		}
	}
	board, err := repo.ReadBoard(ctx, "one", 0)
	if err != nil || winners != 1 || len(board.Members) != 9 || len(board.Entries) != 2 || board.Goal != "满员" {
		t.Fatal(board, winners, err)
	}
	if _, err := db.Sessions().Get(ctx, "b0"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("rejected member exists: %v", err)
	}
	if _, err := repo.AddMembers(ctx, "two", "另一个组", "", build("e", 8)); err != nil {
		t.Fatal(err)
	}
}

func TestCoagentPinPreservesOmittedFieldsAndAcceptsEmptyBody(t *testing.T) {
	ctx := t.Context()
	db := newTestStore(t)
	repo := db.Coagents()
	if err := db.Sessions().Create(ctx, &storage.Session{ID: "main"}); err != nil {
		t.Fatal(err)
	}
	add := func(id, name, goal string) {
		t.Helper()
		_, err := repo.AddMembers(ctx, "main", goal, "", []*storage.Session{{ID: id, Title: name}})
		if err != nil {
			t.Fatal(err)
		}
	}
	check := func(goal, body string, number, members int) {
		t.Helper()
		board, err := repo.ReadBoard(ctx, "a", 0)
		if err != nil || board.Goal != goal || board.PinnedText != body || len(board.Members) != members || len(board.Entries) != number {
			t.Fatalf("board = %+v, err = %v", board, err)
		}
		last := board.Entries[number-1]
		if !last.Pinned || last.TargetSessionID != "" || last.Number != int64(number) {
			t.Fatalf("expected one broadcast pin event, got %+v", last)
		}
	}
	pin := func(goal, body *string) {
		t.Helper()
		_, err := repo.Pin(ctx, "main", true, storage.CoagentPinUpdate{Goal: goal, Text: body})
		if err != nil {
			t.Fatal(err)
		}
	}
	add("a", "修复者", "检查配置")
	check("检查配置", "", 1, 2)
	body := "先认领文件\n再修改"
	pin(nil, &body)
	check("检查配置", body, 2, 2)
	goal := "验证配置"
	pin(&goal, nil)
	check(goal, body, 3, 2)
	add("b", "验收员", goal)
	check(goal, body, 4, 3)
	empty := ""
	pin(nil, &empty)
	check(goal, "", 5, 3)
	next := "交付修复"
	pin(&next, &body)
	check(next, body, 6, 3)
}

func TestCoagentBoardTransactionsAndHandoff(t *testing.T) {
	ctx := t.Context()
	db := newTestStore(t)
	repo := db.Coagents()
	if err := db.Sessions().Create(ctx, &storage.Session{ID: "main"}); err != nil {
		t.Fatal(err)
	}
	members := []*storage.Session{{ID: "a", Title: "监工"}, {ID: "b", Title: "修复者"}}
	if _, err := repo.AddMembers(ctx, "main", "修复配置", "main", members); err != nil {
		t.Fatal(err)
	}
	// An invalid final member must roll back both the first insert and the new goal.
	if _, err := repo.AddMembers(ctx, "main", "意外覆盖", "", []*storage.Session{{ID: "c", Title: "验收员"}, {ID: "d", Title: "监工"}}); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if _, err := db.Sessions().Get(ctx, "c"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("partial create: %v", err)
	}
	if _, err := repo.Post(ctx, "a", false, "outside", "跨组"); err == nil {
		t.Fatal("cross-board target accepted")
	}
	if _, err := repo.Pin(ctx, "b", false, storage.CoagentPinUpdate{Text: new("覆盖")}); err == nil {
		t.Fatal("member changed pin")
	}
	const posts = 12
	var wg sync.WaitGroup
	for range posts {
		wg.Go(func() {
			if _, err := repo.Post(ctx, "a", false, "b", "待验收"); err != nil {
				t.Errorf("post: %v", err)
			}
		})
	}
	wg.Wait()
	for _, old := range []string{"main", "b"} {
		if err := db.Sessions().CreateMaterial(ctx, storage.SessionMaterialCreate{Session: &storage.Session{ID: old + "2"}, CompactFrom: old}); err != nil {
			t.Fatal(err)
		}
	}
	board, err := repo.ReadBoard(ctx, "b2", 1)
	if err != nil {
		t.Fatal(err)
	}
	if board.MainSessionID != "main2" || board.Goal != "修复配置" || len(board.Members) != 3 || len(board.Entries) != posts {
		t.Fatalf("board: %+v", board)
	}
	for i, e := range board.Entries {
		if e.Number != int64(i+2) || e.TargetSessionID != "b" || e.Author != "监工" {
			t.Fatalf("entry: %+v", e)
		}
	}
	if _, err := repo.ReadBoard(ctx, "b", 0); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("retired member: %v", err)
	}
}

func TestCoagentWriteSnapshotsAndRemoval(t *testing.T) {
	ctx := t.Context()
	db := newTestStore(t)
	repo := db.Coagents()
	if err := db.Sessions().Create(ctx, &storage.Session{ID: "main"}); err != nil {
		t.Fatal(err)
	}
	first, err := repo.AddMembers(ctx, "main", "首次", "", []*storage.Session{{ID: "first", Title: "A"}})
	if err != nil {
		t.Fatal(err)
	}
	targeted, err := repo.Post(ctx, "main", true, "first", "这条只给原成员")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.RemoveMember(ctx, "first", "first", false, false); !errors.Is(err, storage.ErrCoagentMainOnly) {
		t.Fatalf("member removal: %v", err)
	}
	if _, err = repo.RemoveMember(ctx, "main", "main", true, false); !errors.Is(err, storage.ErrCoagentTarget) {
		t.Fatalf("remove main: %v", err)
	}
	if _, err = repo.AddMembers(ctx, "main", "冲突", "", []*storage.Session{{ID: "duplicate", Title: "A"}}); !errors.Is(err, storage.ErrCoagentNameConflict) {
		t.Fatal(err)
	}
	removed, err := repo.RemoveMember(ctx, "main", "first", true, false)
	if err != nil {
		t.Fatal(err)
	}
	next, err := repo.AddMembers(ctx, "main", "第二次", "", []*storage.Session{{ID: "next", Title: "A"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed.Members) != 1 || len(removed.Entries) != 1 || removed.Entries[0].Number != 3 || len(next.Members) != 2 || next.Members[1].SessionID != "next" {
		t.Fatalf("removed: %+v; next: %+v", removed, next)
	}
	if first.Goal != "首次" || len(first.Entries) != 1 || first.Entries[0].Number != 1 || len(targeted.Entries) != 1 || targeted.Entries[0].Number != 2 || targeted.Entries[0].TargetSessionID != "first" || targeted.Members[1].SessionID != "first" {
		t.Fatalf("write snapshots changed: %+v %+v", first, targeted)
	}
	if _, err = repo.Post(ctx, "first", false, "", "旧成员发言"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = repo.Post(ctx, "main", true, "first", "过时的目标"); !errors.Is(err, storage.ErrCoagentTarget) {
		t.Fatal(err)
	}
	board, err := repo.ReadBoard(ctx, "next", 0)
	if err != nil || len(board.Entries) != 4 || board.Entries[1].TargetSessionID != "first" {
		t.Fatal(board, err)
	}
	if _, err = db.Sessions().Get(ctx, "first"); err != nil {
		t.Fatal("removed session should survive", err)
	}
}
