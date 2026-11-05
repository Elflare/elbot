// Package events defines process-wide typed signals and their value contracts.
package events

import (
	"context"
	"errors"
	"log/slog"

	"elbot/internal/contextinfo"
	"elbot/internal/signal"
)

// LogCategory selects a log file category. Sources must provide a valid value;
// zero is invalid and never defaults to runtime.
type LogCategory uint8

const (
	LogRuntime LogCategory = iota + 1
	LogAudit
	LogElnis
)

func (value LogCategory) String() string {
	switch value {
	case LogRuntime:
		return "runtime"
	case LogAudit:
		return "audit"
	case LogElnis:
		return "elnis"
	}
	return ""
}

// LogContentType is selected by the source, never inferred from the content.
// Its zero value is ordinary text; JSON content must be valid JSON.
type LogContentType uint8

const (
	ContentText LogContentType = iota
	ContentJSON
)

func (value LogContentType) String() string {
	switch value {
	case ContentText:
		return "text"
	case ContentJSON:
		return "json"
	}
	return ""
}

// ResultStatus describes an operation's result, independently of its log level.
// Sources declare the result; zero means unspecified, not success.
type ResultStatus uint8

const (
	ResultUnspecified ResultStatus = iota
	ResultSucceeded
	ResultFailed
	ResultCanceled
	ResultRejected
	ResultSkipped
)

func (value ResultStatus) String() string {
	switch value {
	case ResultSucceeded:
		return "succeeded"
	case ResultFailed:
		return "failed"
	case ResultCanceled:
		return "canceled"
	case ResultRejected:
		return "rejected"
	case ResultSkipped:
		return "skipped"
	}
	return ""
}

// LogStatus projects an error into a log result only; business control flow must
// use the original error. Cancellation requires every cause to be canceled.
// Severity and known outcomes such as rejection or skipping remain source-owned.
func LogStatus(err error) ResultStatus {
	if err == nil {
		return ResultSucceeded
	}
	if err == context.Canceled {
		return ResultCanceled
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		causes := wrapped.Unwrap()
		for _, cause := range causes {
			if LogStatus(cause) != ResultCanceled {
				return ResultFailed
			}
		}
		if len(causes) > 0 {
			return ResultCanceled
		}
	case interface{ Unwrap() error }:
		if cause := wrapped.Unwrap(); cause != nil {
			return LogStatus(cause)
		}
	}
	return ResultFailed
}

// LogIdentity contains source-supplied associations. Empty strings mean unspecified.
type LogIdentity struct {
	// SessionID identifies the source session.
	SessionID string
	// RunID identifies the run spanning requests.
	RunID string
	// Attempt identifies the attempt within a run.
	Attempt string
	// RequestID identifies the current request.
	RequestID string
	// RootRequestID identifies the root dialogue request.
	RootRequestID string
}

// LogIdentityFromContext copies the execution identity carried by ctx.
func LogIdentityFromContext(ctx context.Context) LogIdentity {
	e, _ := contextinfo.ExecutionFromContext(ctx)
	return LogIdentity{
		SessionID:     e.SessionID,
		RunID:         e.RunID,
		Attempt:       e.Attempt,
		RequestID:     e.RequestID,
		RootRequestID: e.RootRequestID,
	}
}

// LogRecord is the only public log submission contract. The source supplies
// business facts; the logging center assigns reception time before enqueueing.
type LogRecord struct {
	// Category is required from the source; zero and unknown values are invalid.
	Category LogCategory
	// Level is source-defined severity, independent of result; zero means INFO.
	Level slog.Level
	// EventName is a stable source-defined event identifier; empty stays empty.
	EventName string
	// Module names the source module; empty stays empty and does not imply a level.
	Module string
	// Type selects text (zero) or JSON; sources must supply a valid value.
	Type LogContentType
	// Content is source-organized text or valid JSON, including error explanations.
	// Empty text is valid; empty JSON is not. No business objects are accepted.
	Content string
	// ContentLimit is the source's text limit in Unicode characters, including
	// the truncation marker. Zero means 256; negatives are invalid even for JSON.
	// JSON ignores this limit, and sources must bound their JSON size themselves.
	ContentLimit int
	// Identity contains associations explicitly supplied by the source.
	// Empty fields stay empty; publication never fills them from context.
	Identity LogIdentity
	// ResultStatus is source-defined, independent of severity or content. Zero
	// means unspecified and is omitted on disk; sources must supply a valid value.
	ResultStatus ResultStatus
}

// LogSubmitted is stable for the lifetime of the process. Connect here; publish
// through Log or AsyncLog. There is no replay.
var LogSubmitted = signal.New[LogRecord, signal.Empty]("log.submitted")

// Log publishes synchronously. Logging failures never change the business result.
func Log(ctx context.Context, record LogRecord) {
	emission, err := LogSubmitted.EmitAsync(context.WithoutCancel(ctx), record)
	if err != nil && !errors.Is(err, signal.ErrClosed) {
		signal.ReportDiagnostic("log signal admission failed", slog.Any("error", err))
	}
	// Wait for completion; consumers diagnose their own failures.
	if emission != nil {
		_, _ = emission.Wait(context.Background())
	}
}

// AsyncLog publishes without waiting for consumers.
func AsyncLog(ctx context.Context, record LogRecord) {
	_, err := LogSubmitted.EmitAsync(context.WithoutCancel(ctx), record)
	if err != nil && !errors.Is(err, signal.ErrClosed) {
		signal.ReportDiagnostic("log signal admission failed", slog.Any("error", err))
	}
}
