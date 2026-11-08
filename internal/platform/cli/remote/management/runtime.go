package management

import (
	"context"
	"elbot/internal/control"
	"net/http"
)

func (h *handler) getCommands(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.ListCommands(req.Context(), control.ListCommandsRequest{Name: req.URL.Query().Get("name")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) getStatus(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.Status(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) listModels(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.ListModels(req.Context(), control.ListModelsRequest{Query: req.URL.Query().Get("query")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) refreshModels(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.RefreshModels(req.Context(), control.ListModelsRequest{Query: req.URL.Query().Get("query")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) selectModel(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[control.SelectModelRequest](w, req)
	if !ok {
		return
	}
	result, err := h.control.SelectModel(req.Context(), body)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) listRequests(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.ListRequests(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) stopRequest(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.StopRequest(req.Context(), control.StopRequestRequest{RequestID: req.PathValue("request_id")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) stopAllRequests(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.StopAllRequests(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) stopSession(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.StopSession(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) currentConfirmation(w http.ResponseWriter, req *http.Request) {
	refresh, err := boolQuery(req.URL.Query(), "refresh")
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	result, err := h.control.CurrentConfirmation(req.Context(), control.CurrentConfirmationRequest{Refresh: refresh})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
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
	if err != nil {
		writeControlError(w, err)
		return
	}
	h.writeContinuationResult(w, req, result, result.Continuation)
}
func (h *handler) requestRestart(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.RequestRestart(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	if err := writeAPIJSON(w, http.StatusAccepted, map[string]bool{"accepted": result.Accepted}); err != nil {
		if result.Cancel != nil {
			result.Cancel()
		}
		return
	}
	if result.Commit != nil {
		_ = result.Commit(context.WithoutCancel(req.Context()))
	}
}
