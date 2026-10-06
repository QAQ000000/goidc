package httpapi

import (
	"net/http"
	"strconv"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Server) configurationUpgradeOptions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), "SELECT "+productProjection(false)+` FROM products WHERE id=(SELECT product_id FROM services WHERE id=$1 AND user_id=$2 AND NOT import_review_required) AND config_upgrade_enabled`, id, identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	product, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, product)
}

func (s *Server) quoteConfigurationChange(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !s.authLimit(w, r, "configuration-quote", 60) {
		return
	}
	var in store.ConfigurationChangeInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.db.QuoteConfigurationChange(r.Context(), identityFrom(r).ID, id, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusCreated, out)
}

func (s *Server) quoteBillingCycleChange(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !s.authLimit(w, r, "billing-cycle-quote", 60) {
		return
	}
	var in store.BillingCycleChangeInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.db.QuoteBillingCycleChange(r.Context(), identityFrom(r).ID, id, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusCreated, out)
}

func (s *Server) submitConfigurationChange(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		QuoteID int64 `json:"quote_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := s.db.SubmitConfigurationChange(r.Context(), identityFrom(r).ID, id, in.QuoteID, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if out.Replayed {
		status = http.StatusOK
	}
	write(w, status, out)
}

func (s *Server) cancelConfigurationChange(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	changeID, err := strconv.ParseInt(r.PathValue("change_id"), 10, 64)
	if err != nil || changeID < 1 {
		writeError(w, 400, "invalid_configuration_change", "配置变更编号无效")
		return
	}
	if err = s.db.CancelConfigurationChange(r.Context(), identityFrom(r).ID, id, changeID); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]bool{"cancelled": true})
}

func (s *Server) saveConfigurationUpgradePolicy(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.ConfigurationUpgradePolicy
	if !decode(w, r, &in) {
		return
	}
	if err := s.db.SaveConfigurationUpgradePolicy(r.Context(), identityFrom(r).ID, id, in); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"product_id": id})
}

func (s *Server) retryConfigurationChange(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	changeID, err := strconv.ParseInt(r.PathValue("change_id"), 10, 64)
	if err != nil || changeID < 1 {
		writeError(w, 400, "invalid_configuration_change", "配置变更编号无效")
		return
	}
	var in struct{}
	if !decode(w, r, &in) {
		return
	}
	if err = s.db.RetryConfigurationChange(r.Context(), identityFrom(r).ID, id, changeID); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]bool{"queued": true})
}
