package management

import (
	"context"
	"net/http"
)

type connectionContextKey struct{}

func (h *handler) apiMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if h.authenticate == nil {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "认证失败")
			return
		}
		ctx, connection, err := h.authenticate(req.Context(), req)
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "认证失败")
			return
		}
		if h.control == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "not_configured", "管理操作尚未接入")
			return
		}
		ctx = context.WithValue(ctx, connectionContextKey{}, connection)
		next.ServeHTTP(w, req.WithContext(ctx))
	})
}

func (h *handler) requireConnection(w http.ResponseWriter, req *http.Request) bool {
	connection, _ := req.Context().Value(connectionContextKey{}).(bool)
	if !connection {
		writeAPIError(w, http.StatusConflict, "connection_required", "此操作需要当前 WebSocket 连接，请提供 X-CLI-Connection")
	}
	return connection
}
