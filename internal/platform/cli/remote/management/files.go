package management

import (
	"elbot/internal/control"
	"net/http"
	"strconv"
)

func (h *handler) listRollbacks(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.ListRollbacks(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"items": result})
}
func (h *handler) rollbackByID(w http.ResponseWriter, req *http.Request) {
	id, err := strconv.ParseUint(req.PathValue("id"), 10, 64)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_arguments", "撤销编号无效")
		return
	}
	result, err := h.control.RollbackByID(req.Context(), control.RollbackByIDRequest{ID: id})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) listFiles(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.ListFiles(req.Context(), control.ListFilesRequest{Root: req.URL.Query().Get("root"), Path: req.URL.Query().Get("path")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) readFile(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	offset, err := int64Query(q, "offset", 0)
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	limit, err := int64Query(q, "limit", 0)
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	result, err := h.control.ReadFile(req.Context(), control.ReadFileRequest{Root: q.Get("root"), Path: q.Get("path"), Offset: offset, Limit: limit})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) writeFile(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[control.WriteFileRequest](w, req)
	if !ok {
		return
	}
	result, err := h.control.WriteFile(req.Context(), body)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
