package management

import (
	"elbot/internal/control"
	"net/http"
	"strconv"
)

func (h *handler) listQuestions(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.ListQuestions(req.Context(), req.PathValue("session_id"))
	if writeControlResultError(w, result, err) {
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"items": result.Data})
}
func (h *handler) saveQuestionDraft(w http.ResponseWriter, req *http.Request) {
	id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_arguments", "题号无效")
		return
	}
	draft, ok := decodeAPIJSON[control.QuestionDraft](w, req)
	if !ok {
		return
	}
	result, err := h.control.SaveQuestionDraft(req.Context(), req.PathValue("session_id"), id, draft)
	if writeControlResultError(w, result, err) {
		return
	}
	writeAPIJSON(w, http.StatusOK, result.Data)
}
func (h *handler) confirmAnswers(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.ConfirmAnswers(req.Context(), req.PathValue("session_id"))
	if writeControlResultError(w, result, err) {
		return
	}
	h.writeContinuationResult(w, req, result.Data, result.Data.Continuation)
}
