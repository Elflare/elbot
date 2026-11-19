package command

import (
	"context"
	"elbot/internal/delivery"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
)

func TestRouterDispatchReturnsBusinessResults(t *testing.T) {
	router := NewRouter([]string{"/"})
	if err := router.Register(NewFunc(Info{Name: "ping", MinRole: contextinfo.RoleUser}, func(context.Context, Request) (Result, error) {
		return TextResult(operationresult.Success, "pong"), nil
	})); err != nil {
		t.Fatal(err)
	}

	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{Role: contextinfo.RoleUser})
	result, err := router.Dispatch(ctx, "/ping")
	if err != nil || result.Status != operationresult.Success || delivery.ContentBlocksText(result.Blocks) != "pong" {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	result, err = router.Dispatch(ctx, "/missing")
	if err != nil || result.Status != operationresult.Rejected || !strings.Contains(delivery.ContentBlocksText(result.Blocks), "unk command: /missing") {
		t.Fatalf("unknown command result=%+v err=%v", result, err)
	}
}
