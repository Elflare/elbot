package command

import "context"

type Info struct {
	Name        string
	Usage       string
	Description string
	Aliases     []string
	Help        string
}

type Request struct {
	Raw    string
	Prefix string
	Name   string
	Args   string
}

type Result struct {
	Content string
}

type Handler interface {
	Info() Info
	Handle(ctx context.Context, req Request) (*Result, error)
}
