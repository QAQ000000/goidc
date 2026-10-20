package httpapi

import "net/http"

func (s *Server) administratorAccountHistory(w http.ResponseWriter, r *http.Request) {
	if identityFrom(r).Role != "super_admin" || !identityFrom(r).can("audit.view") {
		writeError(w, http.StatusForbidden, "super_admin_required", "历史管理员业务审计需要超级管理员权限")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, http.StatusForbidden, "super_admin_required", "历史管理员业务审计需要有效的超级管理员会话")
		return
	}
	limit, offset := pagination(r)
	result, err := s.db.AdministratorAccountHistory(r.Context(), identityFrom(r).ID, id, tokenHash(cookie.Value), limit, offset)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, result)
}
