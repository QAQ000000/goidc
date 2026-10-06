package httpapi

import (
	"net/http"
	"strconv"

	"github.com/codex/zjmfgo/internal/store"
)

func configurationIDs(w http.ResponseWriter, r *http.Request) (product, group, option int64, ok bool) {
	product, ok = pathID(w, r)
	if !ok {
		return
	}
	for _, name := range []string{"group_id", "option_id"} {
		if raw := r.PathValue(name); raw != "" {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id < 1 {
				writeError(w, 400, "invalid_id", "配置编号无效")
				ok = false
				return
			}
			if name == "group_id" {
				group = id
			} else {
				option = id
			}
		}
	}
	return
}
func (s *Server) saveConfigurationGroup(w http.ResponseWriter, r *http.Request) {
	product, group, _, ok := configurationIDs(w, r)
	if !ok {
		return
	}
	if r.Method == "DELETE" {
		s.deleteConfiguration(w, r, product, group, 0)
		return
	}
	var in store.ProductOptionGroupInput
	if !decodeLimit(w, r, &in, 256<<10) {
		return
	}
	saved, err := s.db.SaveProductOptionGroup(r.Context(), identityFrom(r).ID, product, group, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 200
	if group == 0 {
		status = 201
	}
	write(w, status, map[string]int64{"id": saved, "product_id": product})
}
func (s *Server) saveConfigurationOption(w http.ResponseWriter, r *http.Request) {
	product, group, option, ok := configurationIDs(w, r)
	if !ok {
		return
	}
	if r.Method == "DELETE" {
		s.deleteConfiguration(w, r, product, group, option)
		return
	}
	var in store.ProductOptionInput
	if !decodeLimit(w, r, &in, 256<<10) {
		return
	}
	saved, err := s.db.SaveProductOption(r.Context(), identityFrom(r).ID, product, group, option, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 200
	if option == 0 {
		status = 201
	}
	write(w, status, map[string]int64{"id": saved, "product_id": product, "group_id": group})
}

func (s *Server) replaceOptionPriceTiers(w http.ResponseWriter, r *http.Request) {
	product, group, option, ok := configurationIDs(w, r)
	if !ok || option == 0 || group == 0 {
		return
	}
	var in store.OptionPriceTiersInput
	if !decodeLimit(w, r, &in, 64<<10) {
		return
	}
	if err := s.db.ReplaceOptionPriceTiers(r.Context(), identityFrom(r).ID, product, group, option, in); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"product_id": product, "group_id": group, "option_id": option, "billing_cycle": in.BillingCycle, "tiers": in.Tiers})
}
func (s *Server) deleteConfiguration(w http.ResponseWriter, r *http.Request, product, group, option int64) {
	var in struct {
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.db.DeleteProductConfiguration(r.Context(), identityFrom(r).ID, product, group, option, in.ExpectedRevision); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
