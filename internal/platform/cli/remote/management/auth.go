package management

import "net/http"

func (h *handler) apiMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if h.authenticate == nil {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "认证失败")
			return
		}
		ctx, err := h.authenticate(req.Context(), req)
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "认证失败")
			return
		}
		if h.control == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "not_configured", "管理操作尚未接入")
			return
		}
		next.ServeHTTP(w, req.WithContext(ctx))
	})
}
