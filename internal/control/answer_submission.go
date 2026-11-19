package control

import (
	"context"
	"elbot/internal/operationresult"
)

type AnswerSubmission struct {
	Run    func(context.Context) (context.Context, operationresult.Result[struct{}], error) // Starts the reserved turn after the command receipt.
	Cancel func()                                                                           // Releases the reservation on failed delivery; drafts remain persisted.
}

func (c *Continuation) CancelReservation() {
	if c != nil && c.Answers != nil {
		c.Answers.Cancel()
	}
}
