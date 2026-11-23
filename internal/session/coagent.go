package session

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/operationresult"
	"elbot/internal/storage"
)

// CoagentAction contains the arguments of a single board operation. Session and author identity come from the caller.
type CoagentAction struct {
	Pin             storage.CoagentPinUpdate `json:"-"`
	Operation       string
	Count           int      // Number of names to generate for a manual create command.
	Names           []string `json:"names"`
	Goal            string   `json:"goal"`
	Text            string   `json:"text"`
	TargetSessionID string   `json:"target_session_id"`
	After           int64    `json:"after"` // Exclusive board entry number.
}

// FormatCoagentBoard renders the same snapshot for command output and model tools.
func FormatCoagentBoard(board storage.CoagentBoard) string {
	var result strings.Builder
	fmt.Fprintf(&result, "置顶目标：%s\n成员：", board.Goal)
	for i, m := range board.Members {
		if i > 0 {
			result.WriteString("、")
		}
		fmt.Fprintf(&result, "%s %s", m.SessionID, m.Name)
	}
	fmt.Fprintf(&result, "\n置顶正文：%s", board.PinnedText)
	for _, entry := range board.Entries {
		result.WriteByte('\n')
		result.WriteString(FormatCoagentEntry(entry, board.Members))
	}
	return result.String()
}

func FormatCoagentEntry(entry storage.CoagentEntry, members []storage.CoagentMember) string {
	label := "[" + entry.Author + "]"
	if entry.TargetSessionID != "" {
		name := entry.TargetSessionID
		for _, m := range members {
			if m.SessionID == entry.TargetSessionID {
				name = m.Name
				break
			}
		}
		label += " → [" + name + "]"
	}
	if entry.Pinned {
		label += " 更新置顶"
	}
	return fmt.Sprintf("#%d %s：%s", entry.Number, label, entry.Text)
}

// 创建、写入和读取均以调用者已取得的当前绑定选择协作组。
func (s *Supervisor) CreateCoagents(ctx context.Context, binding *Binding, goal string, names []string, user bool) (operationresult.Result[storage.CoagentEntry], error) {
	ctx, release, err := s.EnterBinding(ctx, binding)
	if err != nil {
		return operationresult.Result[storage.CoagentEntry]{}, err
	}
	defer release()
	current, err := s.CurrentBound(ctx, binding.Scope())
	if err != nil || current.Status != operationresult.Success {
		return operationresult.Result[storage.CoagentEntry]{Status: current.Status, Message: current.Message}, err
	}
	return s.coagents.Create(ctx, current.Data.Session, goal, names, user)
}

func (s *Supervisor) ReadCoagentBoard(ctx context.Context, binding *Binding, after int64) (operationresult.Result[storage.CoagentBoard], error) {
	ctx, release, err := s.EnterBinding(ctx, binding)
	if err != nil {
		return operationresult.Result[storage.CoagentBoard]{}, err
	}
	defer release()
	board, err := s.coagents.Boards.ReadBoard(ctx, binding.SessionID(), after)
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[storage.CoagentBoard]{Status: operationresult.NotFound, Message: "当前会话未加入协作组"}, nil
	}
	if err != nil {
		return operationresult.Result[storage.CoagentBoard]{}, err
	}
	return operationresult.Result[storage.CoagentBoard]{Status: operationresult.Success, Data: board}, nil
}

func (s *Supervisor) PostCoagentEntry(ctx context.Context, binding *Binding, user bool, entry storage.CoagentEntry) (operationresult.Result[storage.CoagentEntry], error) {
	entry.Text = strings.TrimSpace(entry.Text)
	if entry.Text == "" {
		return operationresult.Result[storage.CoagentEntry]{Status: operationresult.InvalidArguments, Message: "留言不能为空"}, nil
	}
	ctx, release, err := s.EnterBinding(ctx, binding)
	if err != nil {
		return operationresult.Result[storage.CoagentEntry]{}, err
	}
	defer release()
	entry, err = s.coagents.Boards.Post(ctx, binding.SessionID(), user, entry)
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[storage.CoagentEntry]{Status: operationresult.NotFound, Message: "当前会话未加入协作组"}, nil
	}
	if err != nil {
		return operationresult.Result[storage.CoagentEntry]{}, err
	}
	return operationresult.Result[storage.CoagentEntry]{Status: operationresult.Success, Data: entry}, nil
}

func (s *Supervisor) PinCoagentBoard(ctx context.Context, binding *Binding, user bool, update storage.CoagentPinUpdate) (operationresult.Result[storage.CoagentEntry], error) {
	if update.Text == nil && update.Goal == nil || update.Goal != nil && strings.TrimSpace(*update.Goal) == "" {
		return operationresult.Result[storage.CoagentEntry]{Status: operationresult.InvalidArguments, Message: "请提供非空目标或置顶正文；正文可为空"}, nil
	}
	ctx, release, err := s.EnterBinding(ctx, binding)
	if err != nil {
		return operationresult.Result[storage.CoagentEntry]{}, err
	}
	defer release()
	entry, err := s.coagents.Boards.Pin(ctx, binding.SessionID(), user, update)
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[storage.CoagentEntry]{Status: operationresult.NotFound, Message: "当前会话未加入协作组"}, nil
	}
	if err != nil {
		return operationresult.Result[storage.CoagentEntry]{}, err
	}
	return operationresult.Result[storage.CoagentEntry]{Status: operationresult.Success, Data: entry}, nil
}
