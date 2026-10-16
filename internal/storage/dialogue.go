package storage

import (
	"context"
	"time"

	"elbot/internal/events"
)

// NativeExchange is an immutable request and the terminal facts of one API call.
// Protocol packages interpret the JSON; storage never imports their item types.
type NativeExchange struct {
	ID, SessionID, APIType, Provider, BaseURL, Model string
	RequestID, RunID, Attempt, PreviousCheckpointID  string
	// RequestJSON contains frozen parameters and ordered durable input references,
	// never the expanded HTTP input. ResponseJSON/ItemsJSON retain native facts.
	RequestJSON, ResponseJSON, ItemsJSON string
	// InputIDsJSON lists only newly consumed inputs, independently of replay input.
	InputIDsJSON  string
	Status, Error string
	CreatedAt     time.Time
}

// NativeInput holds route-owned canonical input and local material references.
// NativeExchange references canonical inputs; media entities live outside SQLite.
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

// ToolBatch retains one assistant call array and its ordered results atomically.
type ToolBatch struct {
	// Call is the assistant message containing the complete ordered call array.
	Call *Message
	// Results follow the original tool-call order, including failed calls.
	Results []*Message
}

// ToolResultCommit replaces a preallocated result without changing its position.
// All attached facts are committed in the same transaction.
type ToolResultCommit struct {
	Call     *Message        // Updated batch header, including prepared arguments and batch status.
	Result   *Message        // Final body in the original slot; nil seals remaining unknown slots on recovery.
	Record   *ToolCallRecord // Raw invocation outcome, unaffected by completion Hooks.
	Metadata SessionMetadata // Tool-owned session fields to merge with the latest row.
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
	// MediaIDs retains the root window's media. Nil and empty mean no references;
	// persistence stores both as a JSON empty array.
	MediaIDs  []string
	Consumed  bool
	CreatedAt time.Time
}

// NativeWindowRecords holds raw storage facts; protocol code rebuilds and validates the window.
type NativeWindowRecords struct {
	// Seed is the root selected by the supplied Session metadata, if present.
	Seed *NativeSeed
	// Checkpoints contains the target and its ancestors, keyed by checkpoint ID.
	Checkpoints map[string]NativeCheckpoint
	// Exchanges contains the calls referenced by that checkpoint chain, keyed by ID.
	Exchanges map[string]NativeExchange
	// Inputs contains consumed inputs and tool results referenced by the target snapshot.
	Inputs map[string]NativeInput
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
	// ElyphRulesShown records rules included in this commit's messages atomically.
	ElyphRulesShown bool
	SessionID       string
	Messages        []*Message
	ToolBatch       *ToolBatch
	ToolResult      *ToolResultCommit
	Native          *NativeCommit
	// PlatformMessageMaps are committed atomically with messages and native state.
	PlatformMessageMaps []PlatformMessageMap
	// MediaReferences retain tool arguments in the same transaction as their calls/results.
	MediaReferences []MediaReference
}

func (c DialogueCommit) MessageRows() []*Message {
	rows := append([]*Message(nil), c.Messages...)
	if c.ToolBatch != nil {
		rows = append(rows, c.ToolBatch.Call)
		rows = append(rows, c.ToolBatch.Results...)
	}
	return rows
}

type DialogueRepository interface {
	// Commit distinguishes storage IO failures from internal validation errors.
	Commit(context.Context, DialogueCommit) (events.LogResult, error)
	CreateExchange(context.Context, *NativeExchange) error
	FinishExchange(context.Context, string, string, string, string, string) error
	CurrentCheckpoint(context.Context, string) (*NativeCheckpoint, error)
	GetCheckpoint(context.Context, string) (*NativeCheckpoint, error)
	CheckpointForMessage(context.Context, string, string) (*NativeCheckpoint, error)
	LoadNativeWindow(context.Context, *Session, *NativeCheckpoint) (*NativeWindowRecords, error)
	PendingInputs(context.Context, string) ([]NativeInput, error)
	Calls(context.Context, string) ([]NativeCall, error)
}
