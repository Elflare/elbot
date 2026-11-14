package management

import (
	"context"
	"elbot/internal/control"
	"log/slog"
	"net/http"

	"elbot/internal/events"
)

func (h *handler) getCommands(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.ListCommands(req.Context(), control.ListCommandsRequest{Name: req.URL.Query().Get("name")})
	if writeControlResultError(w, result, err) {
		return
	}
	writeAPIJSON(w, http.StatusOK, result.Data)
}
func (h *handler) getStatus(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.Status(req.Context())
	if writeControlResultError(w, result, err) {
		return
	}
	writeAPIJSON(w, http.StatusOK, result.Data)
}
func (h *handler) listModels(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.ListModels(req.Context(), control.ListModelsRequest{Query: req.URL.Query().Get("query")})
	if writeControlResultError(w, result, err) {
		return
	}
	writeAPIJSON(w, http.StatusOK, result.Data)
}
func (h *handler) refreshModels(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.RefreshModels(req.Context(), control.ListModelsRequest{Query: req.URL.Query().Get("query")})
	if writeControlResultError(w, result, err) {
		return
	}
	writeAPIJSON(w, http.StatusOK, result.Data)
}
func (h *handler) selectModel(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[control.SelectModelRequest](w, req)
	if !ok {
		return
	}
	result, err := h.control.SelectModel(req.Context(), body)
	if writeControlResultError(w, result, err) {
		return
	}
	writeAPIJSON(w, http.StatusOK, result.Data)
}
func (h *handler) listRequests(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.ListRequests(req.Context())
	if writeControlResultError(w, result, err) {
		return
	}
	writeAPIJSON(w, http.StatusOK, result.Data)
}
func (h *handler) stopRequest(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.StopRequest(req.Context(), control.StopRequestRequest{RequestID: req.PathValue("request_id")})
	if writeControlResultError(w, result, err) {
		return
	}
	writeAPIJSON(w, http.StatusOK, result.Data)
}
func (h *handler) stopAllRequests(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.StopAllRequests(req.Context())
	if writeControlResultError(w, result, err) {
		return
	}
	writeAPIJSON(w, http.StatusOK, result.Data)
}
func (h *handler) stopSession(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.StopSession(req.Context())
	if writeControlResultError(w, result, err) {
		return
	}
	writeAPIJSON(w, http.StatusOK, result.Data)
}
func (h *handler) currentConfirmation(w http.ResponseWriter, req *http.Request) {
	refresh, err := boolQuery(req.URL.Query(), "refresh")
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	result, err := h.control.CurrentConfirmation(req.Context(), control.CurrentConfirmationRequest{Refresh: refresh})
	if writeControlResultError(w, result, err) {
		return
	}
	writeAPIJSON(w, http.StatusOK, result.Data)
}
func (h *handler) respondConfirmation(w http.ResponseWriter, req *http.Request) {
	if !h.requireConnection(w, req) {
		return
	}
	body, ok := decodeAPIJSON[control.RespondConfirmationRequest](w, req)
	if !ok {
		return
	}
	result, err := h.control.RespondConfirmation(req.Context(), body)
	if writeControlResultError(w, result, err) {
		return
	}
	h.writeContinuationResult(w, req, result.Data, result.Data.Continuation)
}
func (h *handler) requestRestart(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.RequestRestart(req.Context())
	if writeControlResultError(w, result, err) {
		return
	}
	restart := result.Data
	err = writeAPIJSON(w, http.StatusAccepted, map[string]bool{"accepted": restart.Accepted})
	if err == nil {
		if _, ok := w.(http.Flusher); ok {
			err = http.NewResponseController(w).Flush()
		}
	}
	if err != nil {
		restart.Cancel()
		events.AsyncLog(req.Context(), events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelError, EventName: "cli_control_send_failed", Module: "cli", Content: err.Error(), ResultStatus: events.LogStatus(err)})
		return
	}
	if err := restart.Commit(context.WithoutCancel(req.Context())); err != nil {
		restart.Cancel()
		events.AsyncLog(req.Context(), events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelError, EventName: "runtime_failed", Module: "cli", Content: err.Error(), ResultStatus: events.LogStatus(err)})
	}
}
