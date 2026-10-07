// Package events defines process-wide typed signals and their value contracts.
package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/signal"
)

type LogCategory string

const (
	LogRuntime LogCategory = "runtime"
	LogAudit   LogCategory = "audit"
	LogElnis   LogCategory = "elnis"
)

type LogRecord struct {
	At       time.Time
	Category LogCategory
	Level    slog.Level
	Name     string
	Module   string
	Summary  string
	Detail   string
	Fields   []slog.Attr
}

type LogDiagnostic struct{ Kind, Detail string }
type DiagnosticError interface{ LogDiagnostic() LogDiagnostic }

// LogSubmitted is stable for the lifetime of the process. Connect here; publish
// through EmitLog so consumers receive an owned snapshot. Consumers must not
// modify the shared record. There is no replay when no consumer is connected.
var LogSubmitted = signal.New[LogRecord]("log.submitted")

// EmitLog reports admission errors, not durability. Logging must never replace
// the caller's business result. The signal reports dispatch failures itself.
func EmitLog(ctx context.Context, record LogRecord) error {
	if record.At.IsZero() {
		record.At = time.Now()
	}
	fields := record.Fields
	record.Fields = nil
	execution, _ := contextinfo.ExecutionFromContext(ctx)
	for _, field := range []slog.Attr{
		slog.String("session_id", execution.SessionID), slog.String("run_id", execution.RunID),
		slog.String("attempt", execution.Attempt), slog.String("request_id", execution.RequestID),
		slog.String("root_request_id", execution.RootRequestID),
	} {
		present := false
		for _, existing := range fields {
			if existing.Key == field.Key {
				field = existing
				present = true
				break
			}
		}
		if present || field.Value.String() != "" {
			record.Fields = append(record.Fields, field)
		}
	}
	// Freeze source identities first so a large payload cannot consume their
	// snapshot budget and cause a fallback to a different context's identity.
	for _, field := range fields {
		switch field.Key {
		case "session_id", "run_id", "attempt", "request_id", "root_request_id":
			continue
		}
		record.Fields = append(record.Fields, field)
	}
	snapshot := logSnapshot{remaining: 16384}
	record.Fields = snapshot.attrs(record.Fields, 0)
	return LogSubmitted.Emit(ctx, record)
}

// A bounded walk also terminates cycles without retaining any original object.
// Unrepresentable values are explicit instead of invoking arbitrary Stringers
// later in the consumer. Strings are immutable and can safely share storage.
type logSnapshot struct{ remaining int }

func (s *logSnapshot) attrs(attrs []slog.Attr, depth int) []slog.Attr {
	result := make([]slog.Attr, 0, min(len(attrs), s.remaining))
	for _, attr := range attrs {
		if s.remaining <= 0 {
			result = append(result, slog.String("snapshot", "[snapshot limit]"))
			break
		}
		s.remaining--
		value := attr.Value.Resolve()
		if depth >= 32 {
			value = slog.StringValue("[snapshot limit]")
		} else if value.Kind() == slog.KindGroup {
			value = slog.GroupValue(s.attrs(value.Group(), depth+1)...)
		} else if value.Kind() == slog.KindAny {
			value = slog.AnyValue(s.value(reflect.ValueOf(value.Any()), depth+1))
		}
		result = append(result, slog.Attr{Key: attr.Key, Value: value})
	}
	return result
}

func (s *logSnapshot) value(v reflect.Value, depth int) any {
	if !v.IsValid() {
		return nil
	}
	if depth >= 32 || s.remaining <= 0 {
		return "[snapshot limit]"
	}
	s.remaining--
	if (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface || v.Kind() == reflect.Map || v.Kind() == reflect.Slice) && v.IsNil() {
		return nil
	}
	if v.CanInterface() {
		switch value := v.Interface().(type) {
		case json.RawMessage:
			var decoded any
			decoder := json.NewDecoder(bytes.NewReader(value))
			decoder.UseNumber()
			if json.Valid(value) && decoder.Decode(&decoded) == nil {
				return s.value(reflect.ValueOf(decoded), depth+1)
			}
			return string(value)
		case json.Number:
			return value
		case time.Time:
			return value
		case error:
			message := value.Error()
			var diagnostic DiagnosticError
			if errors.As(value, &diagnostic) {
				d := diagnostic.LogDiagnostic()
				return map[string]any{"message": message, "kind": d.Kind, "detail": d.Detail}
			}
			return message
		case slog.LogValuer:
			return s.slogValue(slog.AnyValue(value).Resolve(), depth+1)
		case slog.Value:
			return s.slogValue(value.Resolve(), depth+1)
		}
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		return s.value(v.Elem(), depth+1)
	case reflect.Bool:
		return v.Bool()
	case reflect.String:
		return v.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint()
	case reflect.Float32, reflect.Float64:
		return v.Float()
	case reflect.Slice, reflect.Array:
		values := make([]any, 0, min(v.Len(), s.remaining))
		for i := 0; i < v.Len(); i++ {
			if s.remaining <= 0 {
				values = append(values, "[snapshot limit]")
				break
			}
			values = append(values, s.value(v.Index(i), depth+1))
		}
		return values
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return "[unsupported log value]"
		}
		values := make(map[string]any)
		iter := v.MapRange()
		for iter.Next() {
			if s.remaining <= 0 {
				values["snapshot"] = "[snapshot limit]"
				break
			}
			values[iter.Key().String()] = s.value(iter.Value(), depth+1)
		}
		return values
	case reflect.Struct:
		values := make(map[string]any)
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if field.PkgPath != "" {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			if s.remaining <= 0 {
				values["snapshot"] = "[snapshot limit]"
				break
			}
			values[name] = s.value(v.Field(i), depth+1)
		}
		return values
	default:
		return "[unsupported log value]"
	}
}

func (s *logSnapshot) slogValue(value slog.Value, depth int) any {
	if value.Kind() == slog.KindGroup {
		group := make(map[string]any)
		for _, attr := range value.Group() {
			if s.remaining <= 0 {
				group["snapshot"] = "[snapshot limit]"
				break
			}
			group[attr.Key] = s.value(reflect.ValueOf(attr.Value), depth+1)
		}
		return group
	}
	return s.value(reflect.ValueOf(value.Any()), depth+1)
}
