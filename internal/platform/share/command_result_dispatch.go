package share

import (
	"elbot/internal/command"
	"elbot/internal/delivery"
)

func CommandResultOutputs(result command.Result, failure error) []delivery.Output {
	if failure != nil {
		return []delivery.Output{delivery.Text(failure.Error())}
	}
	if len(result.Blocks) == 0 && len(result.Actions) == 0 {
		return nil
	}
	return []delivery.Output{{Kind: delivery.KindText, Blocks: result.Blocks, Actions: result.Actions, FullOperationDetails: true}}
}
func FormatCommandResult(result command.Result) string {
	return delivery.WithActionHints(delivery.ContentBlocksText(result.Blocks), result.Actions)
}
