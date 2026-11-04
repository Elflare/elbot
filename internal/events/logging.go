// Package events defines process-wide typed signals and their value contracts.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func (value LogCategory) Valid() bool { return value >= LogRuntime && value <= LogElnis }

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

func (value LogContentType) Valid() bool { return value == ContentText || value == ContentJSON }

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

func (value ResultStatus) Valid() bool { return value <= ResultSkipped }

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

// LogIdentity contains fixed associations supplied by the business source.
// EmitLog copies supplied pointers but never fills identities from context.
// Subscribers must treat the published pointers as read-only.
type LogIdentity struct {
	// SessionID is the source's session ID; nil is absent, non-nil empty is explicit empty.
	SessionID *string
	// RunID is the source's run ID spanning requests; nil is absent, non-nil empty is explicit empty.
	RunID *string
	// Attempt is the source's attempt within a run; nil is absent, non-nil empty is explicit empty.
	Attempt *string
	// RequestID is the source's current request ID; nil is absent, non-nil empty is explicit empty.
	RequestID *string
	// RootRequestID is the source's root dialogue request ID; nil is absent, non-nil empty is explicit empty.
	RootRequestID *string
}

// LogIdentityFromExecution copies the source-selected execution snapshot.
// All five fields are supplied, including explicit empty values.
func LogIdentityFromExecution(e contextinfo.Execution) LogIdentity {
	return LogIdentity{
		SessionID:     &e.SessionID,
		RunID:         &e.RunID,
		Attempt:       &e.Attempt,
		RequestID:     &e.RequestID,
		RootRequestID: &e.RootRequestID,
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
	// Type selects text (zero) or JSON; unknown values are rejected at publication.
	Type LogContentType
	// Content is source-organized text or valid JSON, including error explanations.
	// Empty text is valid; empty JSON is not. No business objects are accepted.
	Content string
	// ContentLimit is the source's text limit in Unicode characters, including
	// the truncation marker. Zero means 256; negatives are invalid even for JSON.
	// JSON ignores this limit, and sources must bound their JSON size themselves.
	ContentLimit int
	// Identity contains associations explicitly supplied by the source.
	// Zero is fully absent. EmitLog only copies pointers, without context fallback.
	Identity LogIdentity
	// ResultStatus is source-defined, independent of severity or content. Zero
	// means unspecified and is omitted on disk; unknown values are rejected.
	ResultStatus ResultStatus
}

// LogSubmitted is stable for the lifetime of the process. Connect here; publish
// through EmitLog so consumers receive a validated record with copied identities.
// Consumers must not modify the shared record. There is no replay.
var LogSubmitted = signal.New[LogRecord, signal.Empty]("log.submitted")

// EmitLog reports admission errors, not durability. Logging must never replace
// the caller's business result. Logging consumers diagnose their own failures.
func EmitLog(ctx context.Context, record LogRecord) error {
	record, err := prepareLogRecord(record)
	if err != nil {
		return err
	}
	emission, err := LogSubmitted.EmitAsync(context.WithoutCancel(ctx), record)
	if err != nil && !errors.Is(err, signal.ErrClosed) {
		signal.ReportDiagnostic("log signal admission failed", slog.Any("error", err))
	}
	if emission == nil {
		return err
	}
	_, err = emission.Wait(context.Background())
	return err
}

// EmitLogAsync applies the same closed record contract without waiting for the
// logging center's admission. It returns validation and signal admission errors.
func EmitLogAsync(ctx context.Context, record LogRecord) error {
	record, err := prepareLogRecord(record)
	if err != nil {
		return err
	}
	_, err = LogSubmitted.EmitAsync(context.WithoutCancel(ctx), record)
	if err != nil && !errors.Is(err, signal.ErrClosed) {
		signal.ReportDiagnostic("log signal admission failed", slog.Any("error", err))
	}
	return err
}

func prepareLogRecord(record LogRecord) (LogRecord, error) {
	var err error
	switch {
	case !record.Category.Valid():
		err = fmt.Errorf("invalid log category: %d", record.Category)
	case !record.Type.Valid():
		err = fmt.Errorf("invalid log content type: %d", record.Type)
	case !record.ResultStatus.Valid():
		err = fmt.Errorf("invalid log result status: %d", record.ResultStatus)
	case record.ContentLimit < 0:
		err = fmt.Errorf("invalid log content limit: %d", record.ContentLimit)
	case record.Type == ContentJSON && !json.Valid([]byte(record.Content)):
		err = fmt.Errorf("invalid log JSON content")
	}
	if err != nil {
		// Contract failures must remain visible even without a logging center.
		// Never publish recursively or expose source content in diagnostics.
		signal.ReportDiagnostic("invalid log record", slog.Any("error", err))
		return record, err
	}
	for _, field := range []**string{
		&record.Identity.SessionID, &record.Identity.RunID, &record.Identity.Attempt,
		&record.Identity.RequestID, &record.Identity.RootRequestID,
	} {
		if *field != nil {
			value := **field
			*field = &value
		}
	}
	return record, nil
}
