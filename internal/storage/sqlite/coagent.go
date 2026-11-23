package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"

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

// 同一事务分配编号、保存事件并返回本次投递的名册，不让并发写入/压缩改变收件范围。
func appendCoagentEntry(ctx context.Context, tx *sql.Tx, board storage.CoagentBoard, entry storage.CoagentEntry) (storage.CoagentBoard, error) {
	err := tx.QueryRowContext(ctx, `INSERT INTO coagent_entries(board_id,number,author,author_session_id,target_session_id,body,pinned)
 SELECT ?,COALESCE(MAX(number),0)+1,?,?,?,?,? FROM coagent_entries WHERE board_id=? RETURNING number`, board.ID, entry.Author, entry.AuthorSessionID, entry.TargetSessionID, entry.Text, entry.Pinned, board.ID).Scan(&entry.Number)
	if err != nil {
		return storage.CoagentBoard{}, err
	}
	board.Entries = []storage.CoagentEntry{entry}
	return board, tx.Commit()
}

func (r *CoagentRepository) AddMembers(ctx context.Context, main, goal, author string, sessions []*storage.Session) (storage.CoagentBoard, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.CoagentBoard{}, err
	}
	defer tx.Rollback()
	var id int64
	if err = tx.QueryRowContext(ctx, `INSERT INTO coagent_boards(main_session_id,goal) VALUES(?,?) ON CONFLICT(main_session_id) DO UPDATE SET goal=excluded.goal RETURNING id`, main, goal).Scan(&id); err != nil {
		return storage.CoagentBoard{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO coagent_members(session_id,board_id,name) VALUES(?,?,'主会话') ON CONFLICT(session_id) DO NOTHING`, main, id); err != nil {
		return storage.CoagentBoard{}, err
	}
	board, err := readCoagentBoard(ctx, tx, main)
	if err != nil {
		return storage.CoagentBoard{}, err
	}
	if board.ID != id {
		return storage.CoagentBoard{}, storage.ErrCoagentMainOnly
	}
	if len(board.Members)-1+len(sessions) > storage.MaxCoagents {
		return storage.CoagentBoard{}, storage.ErrCoagentLimit
	}
	for _, row := range sessions {
		if err = createSession(ctx, tx, row); err != nil {
			return storage.CoagentBoard{}, err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO coagent_members(session_id,board_id,name) VALUES(?,?,?) ON CONFLICT(board_id,name) DO NOTHING`, row.ID, id, row.Title)
		if err != nil {
			return storage.CoagentBoard{}, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return storage.CoagentBoard{}, err
		}
		if n == 0 {
			return storage.CoagentBoard{}, storage.ErrCoagentNameConflict
		}
		board.Members = append(board.Members, storage.CoagentMember{SessionID: row.ID, Name: row.Title})
	}
	entry := storage.CoagentEntry{Text: goal, Pinned: true, Author: "用户"}
	if author != "" {
		entry.Author = "主会话"
		entry.AuthorSessionID = main
	}
	return appendCoagentEntry(ctx, tx, board, entry)
}

// 授权与编辑在同一个写事务内基于同一份名册，编辑函数只负责本次操作。
func (r *CoagentRepository) writeEntry(ctx context.Context, member string, user bool, edit func(*sql.Tx, *storage.CoagentBoard, *storage.CoagentEntry) error) (storage.CoagentBoard, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.CoagentBoard{}, err
	}
	defer tx.Rollback()
	board, err := readCoagentBoard(ctx, tx, member)
	if err != nil {
		return storage.CoagentBoard{}, err
	}
	entry := storage.CoagentEntry{Author: "用户"}
	if !user {
		for _, m := range board.Members {
			if m.SessionID == member {
				entry.Author, entry.AuthorSessionID = m.Name, member
				break
			}
		}
	}
	if err = edit(tx, &board, &entry); err != nil {
		return storage.CoagentBoard{}, err
	}
	return appendCoagentEntry(ctx, tx, board, entry)
}

func (r *CoagentRepository) Post(ctx context.Context, member string, user bool, targetSessionID, text string) (storage.CoagentBoard, error) {
	return r.writeEntry(ctx, member, user, func(_ *sql.Tx, b *storage.CoagentBoard, e *storage.CoagentEntry) error {
		e.Text, e.TargetSessionID = text, targetSessionID
		if targetSessionID != "" && !slices.ContainsFunc(b.Members, func(m storage.CoagentMember) bool { return m.SessionID == targetSessionID }) {
			return storage.ErrCoagentTarget
		}
		return nil
	})
}
func (r *CoagentRepository) Pin(ctx context.Context, member string, user bool, update storage.CoagentPinUpdate) (storage.CoagentBoard, error) {
	return r.writeEntry(ctx, member, user, func(tx *sql.Tx, b *storage.CoagentBoard, e *storage.CoagentEntry) error {
		if member != b.MainSessionID {
			return storage.ErrCoagentMainOnly
		}
		err := tx.QueryRowContext(ctx, `UPDATE coagent_boards SET goal=COALESCE(?,goal), pinned_text=COALESCE(?,pinned_text) WHERE id=? RETURNING goal,pinned_text`, update.Goal, update.Text, b.ID).Scan(&b.Goal, &b.PinnedText)
		e.Text = fmt.Sprintf("目标：%s\n正文：%s", b.Goal, b.PinnedText)
		e.Pinned = true
		return err
	})
}
func (r *CoagentRepository) RemoveMember(ctx context.Context, member, targetSessionID string, user, retire bool) (storage.CoagentBoard, error) {
	return r.writeEntry(ctx, member, user, func(tx *sql.Tx, b *storage.CoagentBoard, e *storage.CoagentEntry) error {
		if retire {
			if targetSessionID != member {
				return storage.ErrCoagentTarget
			}
		} else if member != b.MainSessionID {
			return storage.ErrCoagentMainOnly
		}
		if targetSessionID == b.MainSessionID {
			return storage.ErrCoagentTarget
		}
		i := slices.IndexFunc(b.Members, func(m storage.CoagentMember) bool { return m.SessionID == targetSessionID })
		if i < 0 {
			return storage.ErrCoagentTarget
		}
		name := b.Members[i].Name
		if _, err := tx.ExecContext(ctx, `DELETE FROM coagent_members WHERE board_id=? AND session_id=?`, b.ID, targetSessionID); err != nil {
			return err
		}
		b.Members = slices.Delete(b.Members, i, i+1)
		e.Pinned = true
		e.Text = fmt.Sprintf("成员 %s（%s）已移出协作组", name, targetSessionID)
		if retire {
			e.Author, e.AuthorSessionID = "系统", ""
			e.Text = fmt.Sprintf("成员 %s（%s）上下文已达阈值，停止工作并退出协作组。", name, targetSessionID)
		}
		return nil
	})
}

func readCoagentBoard(ctx context.Context, tx *sql.Tx, member string) (storage.CoagentBoard, error) {
	var result storage.CoagentBoard
	err := tx.QueryRowContext(ctx, `SELECT b.id,b.main_session_id,b.goal,b.pinned_text FROM coagent_boards b JOIN coagent_members m ON m.board_id=b.id WHERE m.session_id=?`, member).Scan(&result.ID, &result.MainSessionID, &result.Goal, &result.PinnedText)
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
	return result, nil
}
func (r *CoagentRepository) ReadBoard(ctx context.Context, member string, after int64) (storage.CoagentBoard, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return storage.CoagentBoard{}, err
	}
	defer tx.Rollback()
	result, err := readCoagentBoard(ctx, tx, member)
	if err != nil {
		return storage.CoagentBoard{}, err
	}
	if after == math.MaxInt64 {
		return result, tx.Commit()
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
