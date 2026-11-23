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

// ValidateCoagentNames 校验整批创建参数，返回去除首尾空白的成员名，不修改调用者的名单。
func ValidateCoagentNames(goal string, names []string) operationresult.Result[[]string] {
	if strings.TrimSpace(goal) == "" || len(names) == 0 {
		return operationresult.Result[[]string]{Status: operationresult.InvalidArguments, Message: "需要任务目标及成员名字"}
	}
	if len(names) > storage.MaxCoagents {
		return operationresult.Result[[]string]{Status: operationresult.InvalidArguments, Message: storage.ErrCoagentLimit.Error()}
	}
	seen := map[string]bool{"主会话": true, "用户": true, "系统": true}
	clean := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || strings.ContainsAny(name, "\r\n") || seen[name] {
			return operationresult.Result[[]string]{Status: operationresult.InvalidArguments, Message: "成员名应为组内唯一的非空单行文本，不得使用系统署名"}
		}
		seen[name] = true
		clean = append(clean, name)
	}
	return operationresult.Result[[]string]{Status: operationresult.Success, Data: clean}
}

// Co-agent 写入返回提交时的名册和单条事件，读操作按原样返回板历史。
func coagentResult(board storage.CoagentBoard, err error) (operationresult.Result[storage.CoagentBoard], error) {
	var status operationresult.Status
	switch {
	case err == nil:
		return operationresult.Result[storage.CoagentBoard]{Status: operationresult.Success, Data: board}, nil
	case errors.Is(err, storage.ErrNotFound):
		return operationresult.Result[storage.CoagentBoard]{Status: operationresult.NotFound, Message: "当前会话未加入协作组"}, nil
	case errors.Is(err, storage.ErrCoagentMainOnly):
		status = operationresult.Forbidden
	case errors.Is(err, storage.ErrCoagentLimit), errors.Is(err, storage.ErrCoagentNameConflict):
		status = operationresult.Conflict
	case errors.Is(err, storage.ErrCoagentTarget):
		status = operationresult.InvalidArguments
	default:
		return operationresult.Result[storage.CoagentBoard]{}, err
	}
	return operationresult.Result[storage.CoagentBoard]{Status: status, Message: err.Error()}, nil
}

func (s *Supervisor) CreateCoagents(ctx context.Context, binding *Binding, goal string, names []string, user bool) (operationresult.Result[storage.CoagentBoard], error) {
	ctx, release, err := s.EnterBinding(ctx, binding)
	if err != nil {
		return operationresult.Result[storage.CoagentBoard]{}, err
	}
	defer release()
	current, err := s.CurrentBound(ctx, binding.Scope())
	if err != nil || current.Status != operationresult.Success {
		return operationresult.Result[storage.CoagentBoard]{Status: current.Status, Message: current.Message}, err
	}
	result, err := s.coagents.Create(ctx, current.Data.Session, goal, names, user)
	if err != nil {
		return coagentResult(storage.CoagentBoard{}, err)
	}
	return result, nil
}
func (s *Supervisor) ReadCoagentBoard(ctx context.Context, binding *Binding, after int64) (operationresult.Result[storage.CoagentBoard], error) {
	ctx, release, err := s.EnterBinding(ctx, binding)
	if err != nil {
		return operationresult.Result[storage.CoagentBoard]{}, err
	}
	defer release()
	return coagentResult(s.coagents.Boards.ReadBoard(ctx, binding.SessionID(), after))
}
func (s *Supervisor) PostCoagentEntry(ctx context.Context, binding *Binding, user bool, targetSessionID, text string) (operationresult.Result[storage.CoagentBoard], error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return operationresult.Result[storage.CoagentBoard]{Status: operationresult.InvalidArguments, Message: "留言不能为空"}, nil
	}
	ctx, release, err := s.EnterBinding(ctx, binding)
	if err != nil {
		return operationresult.Result[storage.CoagentBoard]{}, err
	}
	defer release()
	return coagentResult(s.coagents.Boards.Post(ctx, binding.SessionID(), user, targetSessionID, text))
}
func (s *Supervisor) PinCoagentBoard(ctx context.Context, binding *Binding, user bool, update storage.CoagentPinUpdate) (operationresult.Result[storage.CoagentBoard], error) {
	if update.Text == nil && update.Goal == nil || update.Goal != nil && strings.TrimSpace(*update.Goal) == "" {
		return operationresult.Result[storage.CoagentBoard]{Status: operationresult.InvalidArguments, Message: "请提供非空目标或置顶正文；正文可为空"}, nil
	}
	ctx, release, err := s.EnterBinding(ctx, binding)
	if err != nil {
		return operationresult.Result[storage.CoagentBoard]{}, err
	}
	defer release()
	return coagentResult(s.coagents.Boards.Pin(ctx, binding.SessionID(), user, update))
}
func (s *Supervisor) RemoveCoagent(ctx context.Context, binding *Binding, targetSessionID string, user, retire bool) (operationresult.Result[storage.CoagentBoard], error) {
	ctx, release, err := s.EnterBinding(ctx, binding)
	if err != nil {
		return operationresult.Result[storage.CoagentBoard]{}, err
	}
	defer release()
	return coagentResult(s.coagents.Boards.RemoveMember(ctx, binding.SessionID(), targetSessionID, user, retire))
}
