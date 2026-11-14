package management

import (
	"elbot/internal/operationresult"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func writeControlResultError[T any](w http.ResponseWriter, result operationresult.Result[T], err error) bool {
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return true
	}
	if result.Status != operationresult.Success {
		writeOperationResultError(w, result.Status, result.Message)
		return true
	}
	return false
}

func writeOperationResultError(w http.ResponseWriter, status operationresult.Status, message string) {
	switch status {
	case operationresult.Rejected:
		writeAPIError(w, http.StatusBadRequest, "operation_rejected", message)
	case operationresult.Failed:
		writeAPIError(w, http.StatusInternalServerError, "operation_failed", message)
	case operationresult.InvalidArguments:
		writeAPIError(w, http.StatusBadRequest, "invalid_arguments", message)
	case operationresult.Forbidden:
		writeAPIError(w, http.StatusForbidden, "permission_denied", message)
	case operationresult.NoCurrentSession:
		writeAPIError(w, http.StatusNotFound, "no_current_session", message)
	case operationresult.NotFound:
		writeAPIError(w, http.StatusNotFound, "not_found", message)
	case operationresult.Conflict:
		writeAPIError(w, http.StatusConflict, "conflict", message)
	case operationresult.Unavailable:
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", message)
	case operationresult.Busy:
		writeAPIError(w, http.StatusConflict, "busy", message)
	case operationresult.Timeout:
		writeAPIError(w, http.StatusGatewayTimeout, "timeout", message)
	case operationresult.Canceled:
		writeAPIError(w, http.StatusRequestTimeout, "canceled", message)
	case operationresult.InvalidTarget:
		writeAPIError(w, http.StatusBadRequest, "invalid_target", message)
	case operationresult.Incompatible:
		writeAPIError(w, http.StatusConflict, "incompatible", message)
	default:
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "unknown operation result status")
	}
}

func normalizeManagementErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		errorWriter := &managementErrorWriter{ResponseWriter: w}
		var wrapped http.ResponseWriter = errorWriter
		if _, ok := w.(http.Flusher); ok {
			wrapped = managementFlushWriter{managementErrorWriter: errorWriter}
		}
		next.ServeHTTP(wrapped, req)
		if errorWriter.errorStatus == http.StatusNotFound {
			writeAPIError(w, http.StatusNotFound, "route_not_found", "接口不存在")
		} else if errorWriter.errorStatus == http.StatusMethodNotAllowed {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不支持")
		}
	})
}

type managementErrorWriter struct {
	http.ResponseWriter
	errorStatus int
	wroteHeader bool
}

func (w *managementErrorWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	if (status == http.StatusNotFound || status == http.StatusMethodNotAllowed) && !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		w.errorStatus = status
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *managementErrorWriter) Write(body []byte) (int, error) {
	if w.errorStatus != 0 {
		return len(body), nil
	}
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *managementErrorWriter) flush() error {
	if w.errorStatus != 0 {
		return nil
	}
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

type managementFlushWriter struct{ *managementErrorWriter }

func (w managementFlushWriter) Flush()                       { _ = w.flush() }
func (w managementFlushWriter) FlushError() error            { return w.flush() }
func (w *managementErrorWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	_ = writeAPIJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeAPIJSON(w http.ResponseWriter, status int, value any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(value)
}

func decodeAPIJSON[T any](w http.ResponseWriter, req *http.Request) (T, bool) {
	var value T
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_arguments", "请求 JSON 无效")
		return value, false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeAPIError(w, http.StatusBadRequest, "invalid_arguments", "请求只能包含一个 JSON 对象")
		return value, false
	}
	return value, true
}
