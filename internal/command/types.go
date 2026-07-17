package command

import (
	"context"

	"elbot/internal/security"
)

type Info struct {
	Name          string
	Usage         string
	Description   string
	Aliases       []string
	Help          string
	SessionEffect SessionEffect
	// MinRole controls slash-command access. RoleUser allows regular users;
	// empty defaults to RoleSuperadmin for backward compatibility.
	MinRole security.Role
}

type SessionEffect uint8

const SessionEffectNone SessionEffect = 0

const (
	SessionEffectSwitchCurrent SessionEffect = 1 << iota
	SessionEffectMutate
)

type Request struct {
	Raw    string
	Prefix string
	Name   string
	Args   string
}

type Result struct {
	Content      string
	Continuation *Continuation
}

type Continuation struct {
	Text      string
	SessionID string
}

type CompletionRequest struct {
	Raw    string
	Prefix string
	Name   string
	Args   string
	Cursor int
}

type Completion struct {
	Text         string
	Label        string
	Description  string
	Kind         string
	ReplaceStart int
	ReplaceEnd   int
}

type Completer interface {
	Complete(ctx context.Context, req CompletionRequest) []Completion
}

type Handler interface {
	Info() Info
	Handle(ctx context.Context, req Request) (*Result, error)
}
