package httpapi

import (
	"net/http"
	"strconv"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) saveNativeConfigurationRule(w http.ResponseWriter, r *http.Request) {
	product, ok := pathID(w, r)
	if !ok {
		return
	}
	var rule int64
	if raw := r.PathValue("rule_id"); raw != "" {
		var err error
		rule, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || rule < 1 {
			writeError(w, 400, "invalid_id", "规则编号无效")
			return
		}
	}
	if r.Method == http.MethodDelete {
		var in struct {
			ExpectedRevision *int64 `json:"expected_revision"`
		}
		if !decode(w, r, &in) {
			return
		}
		if err := s.db.DeleteNativeConfigurationRule(r.Context(), identityFrom(r).ID, product, rule, in.ExpectedRevision); err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, map[string]bool{"deleted": true})
		return
	}
	var in store.NativeConfigurationRuleInput
	if !decodeLimit(w, r, &in, 64<<10) {
		return
	}
	saved, err := s.db.SaveNativeConfigurationRule(r.Context(), identityFrom(r).ID, product, rule, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 200
	if rule == 0 {
		status = 201
	}
	write(w, status, map[string]int64{"id": saved, "product_id": product})
}
