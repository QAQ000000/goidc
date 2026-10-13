package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
)

func (s *Server) adminAffiliateRules(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	out, err := s.db.AffiliateRules(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}
func (s *Server) updateAdminAffiliateRules(w http.ResponseWriter, r *http.Request) {
	var in store.AffiliateRulesInput
	if !decode(w, r, &in) {
		return
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, 401, "unauthorized", "请先登录")
		return
	}
	out, err := s.db.UpdateAffiliateRules(r.Context(), identityFrom(r).ID, tokenHash(cookie.Value), r.Header.Get("Idempotency-Key"), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) adminAffiliateRulesSchema(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	rows, err := s.db.Pool.Query(r.Context(), `SELECT id,name,slug FROM products ORDER BY id`)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rows.Close()
	products := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, slug string
		if err = rows.Scan(&id, &name, &slug); err != nil {
			s.fail(w, r, err)
			return
		}
		products = append(products, map[string]any{"id": id, "name": name, "slug": slug})
	}
	if err = rows.Err(); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"products": products})
}
