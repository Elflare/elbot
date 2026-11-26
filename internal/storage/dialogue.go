package storage

import (
	"context"

	"elbot/internal/storage/maindb"
)

type (
	NativeExchange        = maindb.NativeExchange
	NativeInput           = maindb.NativeInput
	NativeCall            = maindb.NativeCall
	NativeCheckpoint      = maindb.NativeCheckpoint
	ToolBatch             = maindb.ToolBatch
	ToolResultCommit      = maindb.ToolResultCommit
	NativeCommit          = maindb.NativeCommit
	NativeSeed            = maindb.NativeSeed
	NativeWindowRecords   = maindb.NativeWindowRecords
	SessionMaterialCreate = maindb.SessionMaterialCreate
	DialogueCommit        = maindb.DialogueCommit
)

type DialogueRepository interface {
	// Commit distinguishes storage IO failures from internal validation errors.
	CommitDialogue(context.Context, DialogueCommit) error
	CreateExchange(context.Context, *NativeExchange) error
	FinishExchange(context.Context, string, string, string, string, string) error
	CurrentCheckpoint(context.Context, string) (*NativeCheckpoint, error)
	GetCheckpoint(context.Context, string) (*NativeCheckpoint, error)
	CheckpointForMessage(context.Context, string, string) (*NativeCheckpoint, error)
	LoadNativeWindow(context.Context, *Session, *NativeCheckpoint) (*NativeWindowRecords, error)
	PendingInputs(context.Context, string) ([]NativeInput, error)
	Calls(context.Context, string) ([]NativeCall, error)
}

const ToolResultMessageKey = maindb.ToolResultMessageKey
