package management

import (
	"context"
	"elbot/internal/control"
	"elbot/internal/tool"
	"net/http"
)

func (h *handler) queryTools(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	scope := tool.QueryAvailable
	if q.Get("scope") == "catalog" {
		scope = tool.QueryCatalog
	}
	if q.Get("scope") != "" && q.Get("scope") != "catalog" && q.Get("scope") != "available" {
		writeAPIError(w, http.StatusBadRequest, "invalid_arguments", "scope 必须是 available 或 catalog")
		return
	}
	hidden, err := boolQuery(q, "include_hidden")
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	discover, err := boolQuery(q, "include_discover")
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	items, err := h.control.QueryTools(req.Context(), tool.ToolQuery{Scope: scope, Tag: q.Get("tag"), Kind: q.Get("kind"), Names: q["name"], IncludeHidden: hidden, IncludeDiscover: discover})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (h *handler) toolDetail(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.LoadToolDetail(req.Context(), control.ToolDetailRequest{Name: req.PathValue("name")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) reloadSkills(w http.ResponseWriter, req *http.Request) {
	if err := h.control.ReloadSkills(req.Context()); err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (h *handler) removeSkill(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[struct {
		Confirmed bool `json:"confirmed"`
	}](w, req)
	if !ok {
		return
	}
	if err := h.control.RemoveSkill(req.Context(), control.RemoveSkillRequest{Name: req.PathValue("name"), Confirmed: body.Confirmed}); err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (h *handler) listHooks(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.ListHooks(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) getHook(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.GetHook(req.Context(), control.GetHookRequest{HookID: req.PathValue("hook_id")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) reloadHooks(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.ReloadHooks(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}
func (h *handler) startHook(w http.ResponseWriter, req *http.Request) {
	h.hookAction(w, req, h.control.StartHook)
}
func (h *handler) stopHook(w http.ResponseWriter, req *http.Request) {
	h.hookAction(w, req, h.control.StopHook)
}
func (h *handler) restartHook(w http.ResponseWriter, req *http.Request) {
	h.hookAction(w, req, h.control.RestartHook)
}
func (h *handler) hookAction(w http.ResponseWriter, req *http.Request, action func(context.Context, control.GetHookRequest) error) {
	if err := action(req.Context(), control.GetHookRequest{HookID: req.PathValue("hook_id")}); err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
