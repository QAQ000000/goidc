package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

// serviceTrafficProducts only lists packages applicable to the caller's live
// service. Empty applicability means all products, never all customer services.
func (s *Server) serviceTrafficProducts(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var productID int64
	if err := s.db.Pool.QueryRow(r.Context(), "SELECT product_id FROM services WHERE id=$1 AND user_id=$2 AND status IN ('active','suspended') AND NOT import_review_required", id, identityFrom(r).ID).Scan(&productID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.list(w, r, `SELECT tp.id,tp.name,tp.capacity_gb,tp.price_cents,tp.stock,tp.version,'manual'::text AS fulfillment_mode FROM traffic_products tp WHERE tp.active AND (tp.stock IS NULL OR tp.stock>0) AND (NOT EXISTS(SELECT 1 FROM traffic_product_products tpp WHERE tpp.traffic_product_id=tp.id) OR EXISTS(SELECT 1 FROM traffic_product_products tpp WHERE tpp.traffic_product_id=tp.id AND tpp.product_id=$1)) ORDER BY tp.id`, productID)
}

func (s *Server) buyTrafficProduct(w http.ResponseWriter, r *http.Request) {
	serviceID, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		TrafficProductID int64 `json:"traffic_product_id"`
		ExpectedVersion  int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.TrafficProductID < 1 || in.ExpectedVersion < 1 {
		writeError(w, 400, "invalid_traffic_purchase", "流量包或版本无效")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	op := "traffic_order.create:" + strconv.FormatInt(serviceID, 10)
	hash := settingMutationHash(in)
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		// The unique order key is scoped to the customer, including purchases on
		// different services. Lock that same scope before checking any replay.
		if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("traffic:%d:%s", actor, key)); err != nil {
			return txResponse{}, err
		}
		var existingServiceID, existingProductID int64
		err = tx.QueryRow(r.Context(), "SELECT service_id,traffic_product_id FROM traffic_orders WHERE user_id=$1 AND idempotency_key=$2", actor, key).Scan(&existingServiceID, &existingProductID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return txResponse{}, err
		}
		if err == nil && (existingServiceID != serviceID || existingProductID != in.TrafficProductID) {
			return txResponse{}, &txHTTPError{409, "idempotency_conflict", "此幂等键已用于其他服务或流量包"}
		}
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, op, key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{200, replay}, nil
		}
		var productID int64
		if err = tx.QueryRow(r.Context(), "SELECT product_id FROM services WHERE id=$1 AND user_id=$2 AND status IN ('active','suspended') AND NOT import_review_required FOR UPDATE", serviceID, actor).Scan(&productID); err != nil {
			return txResponse{}, err
		}
		var name string
		var capacity, price, version int64
		var stock *int64
		var active bool
		if err = tx.QueryRow(r.Context(), "SELECT name,capacity_gb,price_cents,version,stock,active FROM traffic_products WHERE id=$1 FOR UPDATE", in.TrafficProductID).Scan(&name, &capacity, &price, &version, &stock, &active); err != nil {
			return txResponse{}, err
		}
		if version != in.ExpectedVersion {
			return txResponse{}, &txHTTPError{409, "version_conflict", "流量包已变更，请刷新后重试"}
		}
		var applicable bool
		if err = tx.QueryRow(r.Context(), "SELECT NOT EXISTS(SELECT 1 FROM traffic_product_products WHERE traffic_product_id=$1) OR EXISTS(SELECT 1 FROM traffic_product_products WHERE traffic_product_id=$1 AND product_id=$2)", in.TrafficProductID, productID).Scan(&applicable); err != nil {
			return txResponse{}, err
		}
		if !active || !applicable || stock != nil && *stock < 1 {
			return txResponse{}, &txHTTPError{409, "traffic_product_unavailable", "流量包已停用、库存不足或不适用于当前服务"}
		}
		var invoiceID, orderID int64
		var invoiceNo, orderNo string
		invoiceKey := "traffic:" + strconv.FormatInt(serviceID, 10) + ":" + key
		if err = tx.QueryRow(r.Context(), `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,idempotency_key) VALUES('T-'||replace(gen_random_uuid()::text,'-',''),$1,'unpaid',$2,$3) RETURNING id,invoice_no`, actor, price, invoiceKey).Scan(&invoiceID, &invoiceNo); err != nil {
			return txResponse{}, err
		}
		if _, err = tx.Exec(r.Context(), "INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id) VALUES($1,$2,'traffic',$3,$4)", invoiceID, name+"流量包", price, serviceID); err != nil {
			return txResponse{}, err
		}
		if err = tx.QueryRow(r.Context(), `INSERT INTO traffic_orders(order_no,user_id,traffic_product_id,service_id,invoice_id,product_name,capacity_gb,amount_cents,idempotency_key) VALUES('TO-'||replace(gen_random_uuid()::text,'-',''),$1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,order_no`, actor, in.TrafficProductID, serviceID, invoiceID, name, capacity, price, key).Scan(&orderID, &orderNo); err != nil {
			return txResponse{}, err
		}
		if _, err = tx.Exec(r.Context(), "UPDATE traffic_products SET stock=CASE WHEN stock IS NULL THEN NULL ELSE stock-1 END,sold_count=sold_count+1,version=version+1,updated_at=now() WHERE id=$1", in.TrafficProductID); err != nil {
			return txResponse{}, err
		}
		out := map[string]any{"id": orderID, "order_no": orderNo, "invoice_id": invoiceID, "invoice_no": invoiceNo, "amount_cents": price, "status": "pending", "fulfillment_status": "pending_payment", "version": int64(1)}
		if err = store.Audit(r.Context(), tx, actor, "traffic_order.created", "traffic_order", orderID, map[string]any{"service_id": serviceID, "traffic_product_id": in.TrafficProductID, "invoice_id": invoiceID, "capacity_gb": capacity, "amount_cents": price}); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, op, key, hash, out); err != nil {
			return txResponse{}, err
		}

		return txResponse{201, out}, nil
	})
}

func (s *Server) verifyTrafficDelivery(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64  `json:"expected_version"`
		Note            string `json:"note"`
		Acknowledged    bool   `json:"acknowledged"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if in.ExpectedVersion < 1 || !in.Acknowledged || len([]rune(in.Note)) < 5 || len([]rune(in.Note)) > 2000 {
		writeError(w, 400, "invalid_traffic_delivery", "请确认已在资源端真实交付，并填写至少5字核验说明")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	op := "traffic_order.delivery:" + strconv.FormatInt(id, 10)
	hash := settingMutationHash(in)
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("%d:%s:%s", actor, op, key)); err != nil {
			return txResponse{}, err
		}
		var customerID int64
		if err = tx.QueryRow(r.Context(), "SELECT user_id FROM traffic_orders WHERE id=$1", id).Scan(&customerID); err != nil {
			return txResponse{}, err
		}
		if err = store.LockAdminCustomer(r.Context(), tx, actor, customerID); err != nil {
			return txResponse{}, err
		}
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, op, key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{200, replay}, nil
		}
		var version int64
		var status, fulfillment string
		if err = tx.QueryRow(r.Context(), "SELECT version,status,fulfillment_status FROM traffic_orders WHERE id=$1 AND admin_customer_allowed($2,user_id) FOR UPDATE", id, actor).Scan(&version, &status, &fulfillment); err != nil {
			return txResponse{}, err
		}
		if version != in.ExpectedVersion {
			return txResponse{}, &txHTTPError{409, "version_conflict", "流量包订单已变更，请刷新后重试"}
		}
		if status != "paid" || fulfillment != "waiting_manual" {
			return txResponse{}, &txHTTPError{409, "traffic_delivery_not_ready", "只有已付款且待人工交付的订单可以核验"}
		}
		if _, err = tx.Exec(r.Context(), "UPDATE traffic_orders SET fulfillment_status='fulfilled',fulfillment_note=$2,fulfilled_at=now(),version=version+1,updated_at=now() WHERE id=$1", id, in.Note); err != nil {
			return txResponse{}, err
		}
		out := map[string]any{"id": id, "fulfillment_status": "fulfilled", "version": version + 1}
		if err = store.Audit(r.Context(), tx, actor, "traffic_order.delivery_verified", "traffic_order", id, map[string]any{"note": in.Note, "expected_version": in.ExpectedVersion}); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, op, key, hash, out); err != nil {
			return txResponse{}, err
		}

		return txResponse{200, out}, nil
	})
}
