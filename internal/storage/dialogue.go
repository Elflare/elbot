package storage

import (
	"context"
	"time"
)

// NativeExchange is an immutable request and the terminal facts of one API call.
// Protocol packages interpret the JSON; storage never imports their item types.
type NativeExchange struct {
	ID, SessionID, APIType, Provider, BaseURL, Model string
	RequestID, RunID, Attempt, PreviousCheckpointID  string
	RequestJSON, ResponseJSON, ItemsJSON             string
	InputIDsJSON                                     string
	Status, Error                                    string
	CreatedAt                                        time.Time
}

// NativeInput holds route-owned canonical input and local material references.
// The exact resolved input sent over the wire is retained in NativeExchange.
type NativeInput struct {
	ID, SessionID, MessageID, ExchangeID, CallID string
	ItemJSON, MediaJSON, ConsumedBy              string
	CreatedAt                                    time.Time
}

type NativeCall struct {
	ExchangeID, CallID, Name, Arguments, Status, ResultInputID string
	Ordinal                                                    int
}

type NativeCheckpoint struct {
	ID, SessionID, ParentID, ExchangeID, ResponseID, MessageID string
	SeedID, CallsJSON                                          string
	CreatedAt                                                  time.Time
}

// ToolPair is one immutable display-history unit, committed atomically.
type ToolPair struct {
	Call, Result *Message
}

// NativeCommit advances only a locally committed cursor. ExpectedCheckpointID
// is checked even for the first (empty) cursor, independently of caller gates.
type NativeCommit struct {
	ExpectedCheckpointID string
	Checkpoint           NativeCheckpoint
	// Snapshot retains a historical message boundary without advancing the cursor.
	Snapshot       *NativeCheckpoint
	Inputs         []NativeInput
	Calls          []NativeCall
	ConsumedInputs []string
	ConsumeSeedID  string
}

// NativeSeed owns a complete route-defined root window independently of the
// source session. Storage retains opaque JSON and explicit material references.
type NativeSeed struct {
	ID, SessionID, APIType, Provider, BaseURL             string
	ResponseID, SourceCheckpointID                        string
	ItemsJSON, MaterialsJSON, ContinuationJSON, CallsJSON string
	MediaIDs                                              []string
	Consumed                                              bool
	CreatedAt                                             time.Time
}

// SessionMaterialCreate is the narrow atomic boundary for a new session and
// its prepared material. It exposes neither a transaction nor callbacks.
type SessionMaterialCreate struct {
	Session              *Session
	Messages             []*Message
	Seed                 *NativeSeed
	SourceSessionID      string
	ExpectedCheckpointID string
}

type DialogueCommit struct {
	SessionID string
	Messages  []*Message
	ToolPair  *ToolPair
	Native    *NativeCommit
}

func (c DialogueCommit) MessageRows() []*Message {
	rows := append([]*Message(nil), c.Messages...)
	if c.ToolPair != nil {
		rows = append(rows, c.ToolPair.Call, c.ToolPair.Result)
	}
	return rows
}

type DialogueRepository interface {
	Commit(context.Context, DialogueCommit) error
	CreateExchange(context.Context, *NativeExchange) error
	FinishExchange(context.Context, string, string, string, string, string) error
	GetExchange(context.Context, string) (*NativeExchange, error)
	CurrentCheckpoint(context.Context, string) (*NativeCheckpoint, error)
	GetCheckpoint(context.Context, string) (*NativeCheckpoint, error)
	CheckpointForMessage(context.Context, string, string) (*NativeCheckpoint, error)
	InputsForExchange(context.Context, string) ([]NativeInput, error)
	GetInput(context.Context, string) (*NativeInput, error)
	Seed(context.Context, string) (*NativeSeed, error)
	PendingInputs(context.Context, string) ([]NativeInput, error)
	Calls(context.Context, string) ([]NativeCall, error)
}
