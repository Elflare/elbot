package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"elbot/internal/storage"
)

const coagentSchema = `
CREATE TABLE coagent_boards (
 id INTEGER PRIMARY KEY,
 main_session_id TEXT NOT NULL UNIQUE REFERENCES sessions(id) ON DELETE CASCADE,
 goal TEXT NOT NULL,
 pinned_text TEXT NOT NULL DEFAULT ''
);
CREATE TABLE coagent_members (
 session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
 board_id INTEGER NOT NULL REFERENCES coagent_boards(id) ON DELETE CASCADE,
 name TEXT NOT NULL,
 UNIQUE(board_id,name)
);
CREATE TABLE coagent_entries (
 board_id INTEGER NOT NULL REFERENCES coagent_boards(id) ON DELETE CASCADE,
 number INTEGER NOT NULL,
 author TEXT NOT NULL,
 author_session_id TEXT NOT NULL,
 target_session_id TEXT NOT NULL,
 body TEXT NOT NULL,
 pinned INTEGER NOT NULL,
 PRIMARY KEY(board_id,number)
);
`

type CoagentRepository struct{ db *sql.DB }

func (s *Store) Coagents() storage.CoagentRepository { return &CoagentRepository{s.db} }

func appendCoagentEntry(ctx context.Context, tx *sql.Tx, board int64, entry storage.CoagentEntry) (storage.CoagentEntry, error) {
	err := tx.QueryRowContext(ctx, `INSERT INTO coagent_entries(board_id,number,author,author_session_id,target_session_id,body,pinned)
	SELECT ?,COALESCE(MAX(number),0)+1,?,?,?,?,? FROM coagent_entries WHERE board_id=? RETURNING number`,
		board, entry.Author, entry.AuthorSessionID, entry.TargetSessionID, entry.Text, entry.Pinned, board).Scan(&entry.Number)
	return entry, err
}

func (r *CoagentRepository) AddMembers(ctx context.Context, main, goal, author string, sessions []*storage.Session) (storage.CoagentEntry, error) {
	var entry storage.CoagentEntry
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return entry, err
	}
	defer tx.Rollback()
	var board int64
	if err = tx.QueryRowContext(ctx, `INSERT INTO coagent_boards(main_session_id,goal) VALUES(?,?)
	ON CONFLICT(main_session_id) DO UPDATE SET goal=excluded.goal RETURNING id`, main, goal).Scan(&board); err != nil {
		return entry, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO coagent_members(session_id,board_id,name) VALUES(?,?,'主会话') ON CONFLICT(session_id) DO NOTHING`, main, board); err != nil {
		return entry, err
	}
	var bound int64
	if err = tx.QueryRowContext(ctx, `SELECT board_id FROM coagent_members WHERE session_id=?`, main).Scan(&bound); err != nil {
		return entry, err
	}
	if bound != board {
		return entry, fmt.Errorf("只有主会话可创建成员")
	}
	var members int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM coagent_members WHERE board_id=? AND session_id<>?`, board, main).Scan(&members); err != nil {
		return entry, err
	}
	if len(sessions) > storage.MaxCoagents-members {
		return entry, storage.ErrCoagentLimit
	}
	for _, row := range sessions {
		if err = createSession(ctx, tx, row); err != nil {
			return entry, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO coagent_members(session_id,board_id,name) VALUES(?,?,?)`, row.ID, board, row.Title); err != nil {
			return entry, err
		}
	}
	entry = storage.CoagentEntry{Text: goal, Pinned: true, Author: "用户"}
	if author != "" {
		entry.Author = "主会话"
		entry.AuthorSessionID = main
	}
	entry, err = appendCoagentEntry(ctx, tx, board, entry)
	if err != nil {
		return entry, err
	}
	return entry, tx.Commit()
}

func (r *CoagentRepository) Post(ctx context.Context, member string, user bool, entry storage.CoagentEntry) (storage.CoagentEntry, error) {
	return r.writeEntry(ctx, member, user, entry, nil)
}

func (r *CoagentRepository) Pin(ctx context.Context, member string, user bool, update storage.CoagentPinUpdate) (storage.CoagentEntry, error) {
	return r.writeEntry(ctx, member, user, storage.CoagentEntry{Pinned: true}, &update)
}

func (r *CoagentRepository) writeEntry(ctx context.Context, member string, user bool, entry storage.CoagentEntry, update *storage.CoagentPinUpdate) (storage.CoagentEntry, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return entry, err
	}
	defer tx.Rollback()
	var board int64
	var main string
	err = tx.QueryRowContext(ctx, `SELECT b.id,b.main_session_id,m.name FROM coagent_boards b JOIN coagent_members m ON m.board_id=b.id WHERE m.session_id=?`, member).Scan(&board, &main, &entry.Author)
	if errors.Is(err, sql.ErrNoRows) {
		return entry, storage.ErrNotFound
	}
	if err != nil {
		return entry, err
	}
	entry.AuthorSessionID = member
	if user {
		entry.Author = "用户"
		entry.AuthorSessionID = ""
	}
	if entry.Pinned {
		if member != main {
			return entry, fmt.Errorf("只有主会话可修改置顶")
		}
		entry.TargetSessionID = ""
		if update == nil {
			return entry, fmt.Errorf("置顶更新需要目标或正文")
		}
		var goal, body string
		if err = tx.QueryRowContext(ctx, `UPDATE coagent_boards SET goal=COALESCE(?,goal), pinned_text=COALESCE(?,pinned_text) WHERE id=? RETURNING goal,pinned_text`, update.Goal, update.Text, board).Scan(&goal, &body); err != nil {
			return entry, err
		}
		entry.Text = fmt.Sprintf("目标：%s\n正文：%s", goal, body)
	}
	if entry.TargetSessionID != "" {
		var ok bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM coagent_members WHERE board_id=? AND session_id=?)`, board, entry.TargetSessionID).Scan(&ok); err != nil {
			return entry, err
		}
		if !ok {
			return entry, fmt.Errorf("目标会话不属于本组")
		}
	}
	entry, err = appendCoagentEntry(ctx, tx, board, entry)
	if err != nil {
		return entry, err
	}
	return entry, tx.Commit()
}

func (r *CoagentRepository) ReadBoard(ctx context.Context, member string, after int64) (storage.CoagentBoard, error) {
	var result storage.CoagentBoard
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT b.id,b.main_session_id,b.goal,b.pinned_text FROM coagent_boards b JOIN coagent_members m ON m.board_id=b.id WHERE m.session_id=?`, member).Scan(&result.ID, &result.MainSessionID, &result.Goal, &result.PinnedText)
	if errors.Is(err, sql.ErrNoRows) {
		return result, storage.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT session_id,name FROM coagent_members WHERE board_id=? ORDER BY rowid`, result.ID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var m storage.CoagentMember
		if err := rows.Scan(&m.SessionID, &m.Name); err != nil {
			return result, err
		}
		result.Members = append(result.Members, m)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	entries, err := tx.QueryContext(ctx, `SELECT number,author,author_session_id,target_session_id,body,pinned FROM coagent_entries WHERE board_id=? AND number>? ORDER BY number`, result.ID, after)
	if err != nil {
		return result, err
	}
	defer entries.Close()
	for entries.Next() {
		var e storage.CoagentEntry
		if err := entries.Scan(&e.Number, &e.Author, &e.AuthorSessionID, &e.TargetSessionID, &e.Text, &e.Pinned); err != nil {
			return result, err
		}
		result.Entries = append(result.Entries, e)
	}
	if err := entries.Err(); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
