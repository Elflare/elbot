package management

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"elbot/internal/control"
)

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

func (w *managementErrorWriter) flush() {
	if w.errorStatus != 0 {
		return
	}
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

type managementFlushWriter struct{ *managementErrorWriter }

func (w managementFlushWriter) Flush()                       { w.flush() }
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

func writeControlError(w http.ResponseWriter, err error) {
	var failure *control.Error
	if errors.As(err, &failure) {
		status := http.StatusInternalServerError
		switch failure.Code {
		case "invalid_arguments":
			status = http.StatusBadRequest
		case "permission_denied":
			status = http.StatusForbidden
		case "not_found", "no_current_session":
			status = http.StatusNotFound
		case "session_busy", "mode_conflict", "file_changed", "binding_changed", "confirmation_required", "confirmation_expired":
			status = http.StatusConflict
		case "timeout":
			status = http.StatusGatewayTimeout
		case "canceled":
			status = http.StatusRequestTimeout
		case "not_configured":
			status = http.StatusServiceUnavailable
		}
		writeAPIError(w, status, failure.Code, failure.Message)
		return
	}
	classified := control.ClassifyError(err)
	var classifiedFailure *control.Error
	if errors.As(classified, &classifiedFailure) {
		writeControlError(w, classifiedFailure)
		return
	}
	writeAPIError(w, http.StatusInternalServerError, "internal_error", "操作执行失败")
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
