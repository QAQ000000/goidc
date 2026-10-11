package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

type trafficProductInput struct {
	Name                 string  `json:"name"`
	CapacityGB           int64   `json:"capacity_gb"`
	PriceCents           int64   `json:"price_cents"`
	Active               bool    `json:"active"`
	Stock                *int64  `json:"stock"`
	ApplicableProductIDs []int64 `json:"applicable_product_ids"`
	ExpectedVersion      int64   `json:"expected_version"`
}

func validateTrafficProduct(in *trafficProductInput, create bool) bool {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 120 || in.CapacityGB < 1 || in.CapacityGB > 1000000000 || in.PriceCents < 1 || in.PriceCents > 100000000000 || (in.Stock != nil && (*in.Stock < 0 || *in.Stock > 100000000000)) {
		return false
	}
	seen := map[int64]bool{}
	for _, id := range in.ApplicableProductIDs {
		if id < 1 || seen[id] {
			return false
		}
		seen[id] = true
	}
	if len(in.ApplicableProductIDs) > 500 {
		return false
	}
	return create && in.ExpectedVersion == 0 || !create && in.ExpectedVersion > 0
}

func (s *Server) trafficProducts(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.trafficProductList(w, r)
		return
	}
	id := int64(0)
	if r.Method == http.MethodPatch || r.Method == http.MethodDelete {
		var ok bool
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	var in trafficProductInput
	if !decode(w, r, &in) {
		return
	}
	if r.Method == http.MethodDelete {
		if in.ExpectedVersion < 1 {
			writeError(w, http.StatusBadRequest, "invalid_traffic_product", "版本无效")
			return
		}
	} else if !validateTrafficProduct(&in, id == 0) {
		writeError(w, http.StatusBadRequest, "invalid_traffic_product", "流量包名称、容量、价格、库存或版本无效")
		return
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	operation := "traffic_product.create"
	if id > 0 {
		operation = "traffic_product.update:" + strconv.FormatInt(id, 10)
		if r.Method == http.MethodDelete {
			operation = "traffic_product.delete:" + strconv.FormatInt(id, 10)
		}
	}
	hash := settingMutationHash(struct {
		ID    int64
		Input trafficProductInput
	}{id, in})
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("%d:%s:%s", actor, operation, key)); err != nil {
		s.fail(w, r, err)
		return
	}
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, http.StatusOK, replay)
		return
	}
	if id == 0 {
		if err = ensureTrafficProductLinks(r, tx, in.ApplicableProductIDs); err != nil {
			s.fail(w, r, err)
			return
		}
		var created int64
		err = tx.QueryRow(r.Context(), `INSERT INTO traffic_products(name,capacity_gb,price_cents,active,stock) VALUES($1,$2,$3,$4,$5) RETURNING id`, in.Name, in.CapacityGB, in.PriceCents, in.Active, in.Stock).Scan(&created)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if err = replaceTrafficProductLinks(r, tx, created, in.ApplicableProductIDs); err != nil {
			s.fail(w, r, err)
			return
		}
		result := map[string]any{"id": created, "version": int64(1)}
		if err = store.Audit(r.Context(), tx, actor, "traffic_product.created", "traffic_product", created, map[string]any{"name": in.Name, "capacity_gb": in.CapacityGB, "price_cents": in.PriceCents}); err != nil {
			s.fail(w, r, err)
			return
		}
		if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
			s.fail(w, r, err)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, http.StatusCreated, result)
		return
	}
	var version int64
	if err = tx.QueryRow(r.Context(), "SELECT version FROM traffic_products WHERE id=$1 FOR UPDATE", id).Scan(&version); err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "not_found", "流量包不存在")
			return
		}
		s.fail(w, r, err)
		return
	}
	if version != in.ExpectedVersion {
		writeError(w, http.StatusConflict, "version_conflict", "流量包已变更，请刷新后重试")
		return
	}
	if r.Method == http.MethodDelete {
		var count int64
		if err = tx.QueryRow(r.Context(), "SELECT count(*) FROM traffic_orders WHERE traffic_product_id=$1", id).Scan(&count); err != nil {
			s.fail(w, r, err)
			return
		}
		if count > 0 {
			writeError(w, http.StatusConflict, "traffic_product_in_use", "已有流量包订单，不能删除；请停用后保留历史记录")
			return
		}
		if _, err = tx.Exec(r.Context(), "DELETE FROM traffic_products WHERE id=$1", id); err != nil {
			s.fail(w, r, err)
			return
		}
		result := map[string]any{"id": id, "deleted": true}
		if err = store.Audit(r.Context(), tx, actor, "traffic_product.deleted", "traffic_product", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
			s.fail(w, r, err)
			return
		}
		if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
			s.fail(w, r, err)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, http.StatusOK, result)
		return
	}
	if err = ensureTrafficProductLinks(r, tx, in.ApplicableProductIDs); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE traffic_products SET name=$2,capacity_gb=$3,price_cents=$4,active=$5,stock=$6,version=version+1,updated_at=now() WHERE id=$1`, id, in.Name, in.CapacityGB, in.PriceCents, in.Active, in.Stock); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = replaceTrafficProductLinks(r, tx, id, in.ApplicableProductIDs); err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": id, "version": version + 1}
	if err = store.Audit(r.Context(), tx, actor, "traffic_product.updated", "traffic_product", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}

func ensureTrafficProductLinks(r *http.Request, tx pgx.Tx, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	var count int
	if err := tx.QueryRow(r.Context(), "SELECT count(*) FROM products WHERE id=ANY($1)", ids).Scan(&count); err != nil {
		return err
	}
	if count != len(ids) {
		return &store.Conflict{Code: "invalid_product", Message: "适用产品包含不存在的商品"}
	}
	return nil
}

func replaceTrafficProductLinks(r *http.Request, tx pgx.Tx, id int64, productIDs []int64) error {
	if _, err := tx.Exec(r.Context(), "DELETE FROM traffic_product_products WHERE traffic_product_id=$1", id); err != nil {
		return err
	}
	for _, productID := range productIDs {
		if _, err := tx.Exec(r.Context(), "INSERT INTO traffic_product_products(traffic_product_id,product_id) VALUES($1,$2)", id, productID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) trafficProductList(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	active := strings.TrimSpace(r.URL.Query().Get("active"))
	if active != "" && active != "true" && active != "false" {
		writeError(w, http.StatusBadRequest, "invalid_active", "启用状态无效")
		return
	}
	tx, err := s.db.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var total int64
	if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM traffic_products tp WHERE ($1='' OR tp.name ILIKE '%'||$1||'%') AND ($2='' OR tp.active=($2='true'))`, q, active).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT tp.id,tp.name,tp.capacity_gb,tp.price_cents,tp.active,tp.stock,tp.sold_count,tp.version,tp.created_at,tp.updated_at,
COALESCE(array_agg(tpp.product_id ORDER BY tpp.product_id) FILTER (WHERE tpp.product_id IS NOT NULL), '{}') AS applicable_product_ids,
COALESCE(jsonb_agg(jsonb_build_object('id',p.id,'name',p.name) ORDER BY p.id) FILTER (WHERE p.id IS NOT NULL), '[]'::jsonb) AS applicable_products
FROM traffic_products tp LEFT JOIN traffic_product_products tpp ON tpp.traffic_product_id=tp.id LEFT JOIN products p ON p.id=tpp.product_id
WHERE ($1='' OR tp.name ILIKE '%'||$1||'%') AND ($2='' OR tp.active=($2='true'))
GROUP BY tp.id ORDER BY tp.id DESC LIMIT $3 OFFSET $4`, q, active, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) trafficOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.PathValue("id") != "" {
		s.trafficOrderDetail(w, r)
		return
	}
	if r.Method == http.MethodPost && r.PathValue("id") != "" && strings.HasSuffix(r.URL.Path, "/cancel") {
		s.cancelTrafficOrder(w, r)
		return
	}
	limit, offset := pagination(r)
	args := []any{identityFrom(r).ID}
	where := "admin_customer_allowed($1,t.user_id)"
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		if status != "pending" && status != "paid" && status != "cancelled" && status != "refunded" {
			writeError(w, http.StatusBadRequest, "invalid_status", "流量包订单状态无效")
			return
		}
		args = append(args, status)
		where += fmt.Sprintf(" AND t.status=$%d", len(args))
	}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		if len(q) > 120 {
			writeError(w, http.StatusBadRequest, "invalid_query", "搜索条件过长")
			return
		}
		args = append(args, "%"+q+"%")
		where += fmt.Sprintf(" AND (t.order_no ILIKE $%d OR t.product_name ILIKE $%d OR u.display_name ILIKE $%d OR u.email ILIKE $%d)", len(args), len(args), len(args), len(args))
	}
	projection := `t.id,t.order_no,t.user_id,u.display_name AS customer_name,t.traffic_product_id,t.product_name,t.capacity_gb,t.amount_cents,t.status,t.payment_method,t.fulfillment_status,t.fulfillment_note,t.fulfilled_at,t.service_id,s.domain AS service_domain,t.invoice_id,t.version,t.created_at,t.updated_at`
	s.transactionPage(w, r, projection, " FROM traffic_orders t JOIN users u ON u.id=t.user_id LEFT JOIN services s ON s.id=t.service_id WHERE "+where, "t.id", args, limit, offset)
}

func (s *Server) trafficOrderDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), `SELECT t.id,t.order_no,t.user_id,u.display_name AS customer_name,u.email,t.traffic_product_id,t.product_name,t.capacity_gb,t.amount_cents,t.status,t.payment_method,t.fulfillment_status,t.fulfillment_note,t.fulfilled_at,t.service_id,s.domain AS service_domain,t.invoice_id,t.version,t.created_at,t.updated_at FROM traffic_orders t JOIN users u ON u.id=t.user_id LEFT JOIN services s ON s.id=t.service_id WHERE t.id=$1 AND admin_customer_allowed($2,t.user_id)`, id, identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "not_found", "流量包订单不存在")
			return
		}
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) cancelTrafficOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) || in.ExpectedVersion < 1 {
		writeError(w, http.StatusBadRequest, "invalid_traffic_order", "版本无效")
		return
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	operation := "traffic_order.cancel:" + strconv.FormatInt(id, 10)
	hash := settingMutationHash(struct {
		ID int64
		In any
	}{id, in})
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("%d:%s:%s", actor, operation, key)); err != nil {
		s.fail(w, r, err)
		return
	}
	var invoiceID int64
	var customerID int64
	if err = tx.QueryRow(r.Context(), "SELECT user_id FROM traffic_orders WHERE id=$1", id).Scan(&customerID); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.LockAdminCustomer(r.Context(), tx, actor, customerID); err != nil {
		s.fail(w, r, err)
		return
	}
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, http.StatusOK, replay)
		return
	}
	if err = tx.QueryRow(r.Context(), "SELECT invoice_id FROM traffic_orders WHERE id=$1 AND admin_customer_allowed($2,user_id)", id, actor).Scan(&invoiceID); err != nil {
		s.fail(w, r, err)
		return
	}
	var invoiceStatus string
	if err = tx.QueryRow(r.Context(), "SELECT status FROM invoices WHERE id=$1 FOR UPDATE", invoiceID).Scan(&invoiceStatus); err != nil {
		s.fail(w, r, err)
		return
	}
	var version int64
	var status string
	if err = tx.QueryRow(r.Context(), "SELECT version,status FROM traffic_orders WHERE id=$1 AND admin_customer_allowed($2,user_id) FOR UPDATE", id, actor).Scan(&version, &status); err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "not_found", "流量包订单不存在")
			return
		}
		s.fail(w, r, err)
		return
	}
	if version != in.ExpectedVersion {
		writeError(w, http.StatusConflict, "version_conflict", "流量包订单已变更，请刷新后重试")
		return
	}
	if status != "pending" {
		writeError(w, http.StatusConflict, "traffic_order_not_cancellable", "只有待支付流量包订单可以取消")
		return
	}
	if invoiceStatus != "unpaid" {
		writeError(w, 409, "traffic_order_not_cancellable", "只有待支付流量包订单可以取消")
		return
	}
	var paymentPending bool
	if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM payment_transactions WHERE invoice_id=$1 AND status IN ('pending','expired') AND provider<>'mock')", invoiceID).Scan(&paymentPending); err != nil {
		s.fail(w, r, err)
		return
	}
	if paymentPending {
		writeError(w, 409, "payment_pending", "存在未确认的真实付款，先查单后再取消")
		return
	}
	if err = store.ReleaseTrafficInvoice(r.Context(), tx, invoiceID, "cancelled"); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE invoices SET status='cancelled' WHERE id=$1", invoiceID); err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": id, "status": "cancelled", "version": version + 1}
	if err = store.Audit(r.Context(), tx, actor, "traffic_order.cancelled", "traffic_order", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}
