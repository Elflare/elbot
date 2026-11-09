package management

import (
	"net/http"

	"elbot/internal/control"
)

func (h *handler) getWorkspace(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.Workspace(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (h *handler) workspaceDirectories(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.WorkspaceDirectories(req.Context(), control.WorkspaceDirectoriesRequest{Path: req.URL.Query().Get("path")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (h *handler) setUserWorkspace(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[control.SetUserWorkspaceRequest](w, req)
	if !ok {
		return
	}
	result, err := h.control.SetUserWorkspace(req.Context(), body)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (h *handler) setWorkspaceSync(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[control.SetWorkspaceSyncRequest](w, req)
	if !ok {
		return
	}
	result, err := h.control.SetWorkspaceSync(req.Context(), body)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (h *handler) workspaceGit(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.WorkspaceGit(req.Context(), control.WorkspaceGitRequest{Side: req.URL.Query().Get("side")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (h *handler) workspaceGitDiff(w http.ResponseWriter, req *http.Request) {
	diff, err := h.control.WorkspaceGitDiff(req.Context(), control.WorkspaceGitRequest{Side: req.URL.Query().Get("side"), Path: req.URL.Query().Get("path")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]string{"diff": diff})
}
