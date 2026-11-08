package management

import (
	"elbot/internal/control"
	"net/http"
)

func (h *handler) listSessions(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	page, err := intQuery(q, "page", 1)
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	pageSize, err := intQuery(q, "page_size", 20)
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	archived, err := boolQuery(q, "archived")
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	exclude, err := boolQuery(q, "exclude_current")
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	result, err := h.control.ListSessions(req.Context(), control.ListSessionsRequest{Query: q.Get("query"), Page: page, PageSize: pageSize, Archived: archived, ExcludeCurrent: exclude})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) newSession(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.NewSession(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) getSession(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.GetSession(req.Context(), control.GetSessionRequest{SessionID: req.PathValue("session_id")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) sessionMessages(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	page, err := intQuery(q, "page", 1)
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	pageSize, err := intQuery(q, "page_size", 20)
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	result, err := h.control.SessionMessages(req.Context(), control.SessionMessagesRequest{SessionID: req.PathValue("session_id"), Page: page, PageSize: pageSize})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) resumeSession(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[struct {
		MessageLimit int `json:"message_limit"`
	}](w, req)
	if !ok {
		return
	}
	result, err := h.control.ResumeSession(req.Context(), control.ResumeSessionRequest{SessionID: req.PathValue("session_id"), MessageLimit: body.MessageLimit})
	if err != nil {
		writeControlError(w, err)
		return
	}
	h.writeContinuationResult(w, req, result, result.Continuation)
}
func (h *handler) forkSession(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[struct {
		MessageID    string `json:"message_id"`
		MessageLimit int    `json:"message_limit"`
	}](w, req)
	if !ok {
		return
	}
	result, err := h.control.ForkSession(req.Context(), control.ForkSessionRequest{SessionID: req.PathValue("session_id"), MessageID: body.MessageID, MessageLimit: body.MessageLimit})
	if err != nil {
		writeControlError(w, err)
		return
	}
	h.writeContinuationResult(w, req, result, result.Continuation)
}
func (h *handler) renameSession(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[struct {
		Title string `json:"title"`
	}](w, req)
	if !ok {
		return
	}
	result, err := h.control.RenameSession(req.Context(), control.RenameSessionRequest{Target: req.PathValue("session_id"), Title: body.Title})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) archiveSession(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[struct {
		Confirmed bool `json:"confirmed"`
	}](w, req)
	if !ok {
		return
	}
	if !body.Confirmed {
		writeAPIError(w, http.StatusBadRequest, "confirmation_required", "归档会话需要确认")
		return
	}
	result, err := h.control.SetSessionArchived(req.Context(), control.SetSessionArchivedRequest{SessionID: req.PathValue("session_id"), Archived: true, Confirmed: body.Confirmed})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) unarchiveSession(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.SetSessionArchived(req.Context(), control.SetSessionArchivedRequest{SessionID: req.PathValue("session_id"), Archived: false})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) pinSession(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.SetSessionPinned(req.Context(), control.SetSessionPinnedRequest{SessionID: req.PathValue("session_id"), Pinned: true})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) unpinSession(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.SetSessionPinned(req.Context(), control.SetSessionPinnedRequest{SessionID: req.PathValue("session_id"), Pinned: false})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) deleteSession(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[struct {
		Confirmed bool `json:"confirmed"`
	}](w, req)
	if !ok {
		return
	}
	if !body.Confirmed {
		writeAPIError(w, http.StatusBadRequest, "confirmation_required", "删除会话需要确认")
		return
	}
	result, err := h.control.DeleteSession(req.Context(), control.DeleteSessionRequest{SessionID: req.PathValue("session_id"), Confirmed: body.Confirmed})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) cleanSessions(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[control.CleanSessionsRequest](w, req)
	if !ok {
		return
	}
	result, err := h.control.CleanSessions(req.Context(), body)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) compactSession(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.CompactSession(req.Context(), control.CompactSessionRequest{SessionID: req.PathValue("session_id")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) setMode(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[struct {
		Mode            string `json:"mode"`
		NewSessionTitle string `json:"new_session_title"`
	}](w, req)
	if !ok {
		return
	}
	if body.NewSessionTitle == "" {
		switch body.Mode {
		case "chat":
			body.NewSessionTitle = "New chat session"
		case "work":
			body.NewSessionTitle = "New work session"
		}
	}
	result, err := h.control.SetMode(req.Context(), control.SetModeRequest{Mode: body.Mode, NewSessionTitle: body.NewSessionTitle})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
