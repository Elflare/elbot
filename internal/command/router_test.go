package command

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/operationresult"
)

func TestRouterDispatchReturnsBusinessResults(t *testing.T) {
	router := NewRouter([]string{"/"})
	if err := router.Register(NewFunc(Info{Name: "ping"}, func(context.Context, Request) (Result, error) {
		return Result{Status: operationresult.Success, Kind: ResultKindText, Data: TextData{Content: "pong"}}, nil
	})); err != nil {
		t.Fatal(err)
	}

	result, err := router.Dispatch(t.Context(), "/ping")
	if err != nil || result.Status != operationresult.Success || result.Data.(TextData).Content != "pong" {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	result, err = router.Dispatch(t.Context(), "/missing")
	if err != nil || result.Status != operationresult.Rejected || !strings.Contains(result.Message, "unk command: /missing") {
		t.Fatalf("unknown command result=%+v err=%v", result, err)
	}
}
