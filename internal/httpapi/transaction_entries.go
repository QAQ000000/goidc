package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) adminTransactionEntries(w http.ResponseWriter, r *http.Request) {
	filter, ok := transactionEntriesFilter(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r)
	page, err := s.db.AdminTransactionEntryPage(r.Context(), identityFrom(r).ID, filter, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, page)
}
func (s *Server) adminTransactionSummary(w http.ResponseWriter, r *http.Request) {
	filter, ok := transactionEntriesFilter(w, r)
	if !ok {
		return
	}
	result, err := s.db.AdminTransactionEntrySummary(r.Context(), identityFrom(r).ID, filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, result)
}
func transactionEntriesFilter(w http.ResponseWriter, r *http.Request) (store.AdminTransactionFilter, bool) {
	q := r.URL.Query()
	keyword, kind, direction, channel := strings.TrimSpace(q.Get("q")), q.Get("kind"), q.Get("direction"), strings.TrimSpace(q.Get("channel"))
	invalid := func() { writeError(w, http.StatusBadRequest, "invalid_transaction_filter", "交易流水筛选无效") }
	if len(keyword) > 120 || len(channel) > 100 || (kind != "" && kind != "topup" && kind != "product" && kind != "renew" && kind != "other") || (direction != "" && direction != "income" && direction != "expense" && direction != "neutral") {
		invalid()
		return store.AdminTransactionFilter{}, false
	}
	var customerID int64
	if raw := q.Get("customer_id"); raw != "" {
		var err error
		customerID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || customerID < 1 {
			invalid()
			return store.AdminTransactionFilter{}, false
		}
	}
	var start, end *time.Time
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	for _, key := range []string{"from", "to"} {
		if raw := q.Get(key); raw != "" {
			v, err := time.ParseInLocation("2006-01-02", raw, zone)
			if err != nil {
				invalid()
				return store.AdminTransactionFilter{}, false
			}
			if key == "from" {
				start = &v
			} else {
				v = v.AddDate(0, 0, 1)
				end = &v
			}
		}
	}
	if start != nil && end != nil && !start.Before(*end) {
		invalid()
		return store.AdminTransactionFilter{}, false
	}
	var minimum, maximum *int64
	for _, key := range []string{"min_cents", "max_cents"} {
		if raw := q.Get(key); raw != "" {
			v, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || v < 0 {
				invalid()
				return store.AdminTransactionFilter{}, false
			}
			if key == "min_cents" {
				minimum = &v
			} else {
				maximum = &v
			}
		}
	}
	if minimum != nil && maximum != nil && *minimum > *maximum {
		invalid()
		return store.AdminTransactionFilter{}, false
	}
	return store.AdminTransactionFilter{Keyword: keyword, CustomerID: customerID, Kind: kind, Direction: direction, Channel: channel, Start: start, End: end, Minimum: minimum, Maximum: maximum}, true
}
