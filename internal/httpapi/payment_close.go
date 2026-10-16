package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) paymentCloseHTTP(next http.HandlerFunc) http.HandlerFunc {
	authenticated := s.sessionAuth(next, true)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		authenticated(w, r)
	}
}

type paymentCloseInput struct {
	ExpectedVersion int64 `json:"expected_version"`
}

func closePaymentSessionHash(r *http.Request) string {
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		return ""
	}
	return tokenHash(cookie.Value)
}

func paymentClosePayload(op store.PaymentCloseOperation) map[string]any {
	return map[string]any{
		"id":                      op.ID,
		"payment_id":              op.PaymentID,
		"invoice_id":              op.InvoiceID,
		"provider":                op.Provider,
		"provider_payment_id":     op.ProviderPaymentID,
		"amount_cents":            op.AmountCents,
		"status":                  op.Status,
		"result_code":             op.ResultCode,
		"operation_id":            op.OperationID,
		"external_transaction_id": op.ExternalTransactionID,
		"replayed":                op.Replayed,
		"version":                 op.Version,
	}
}

func (s *Server) closePayment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	invoiceID, ok := pathID(w, r)
	if !ok {
		return
	}
	paymentID, err := strconv.ParseInt(r.PathValue("payment_id"), 10, 64)
	if err != nil || paymentID < 1 {
		writeError(w, http.StatusBadRequest, "invalid_id", "支付单编号无效")
		return
	}
	var input paymentCloseInput
	if !decode(w, r, &input) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	op, err := s.db.BeginPaymentClose(ctx, identityFrom(r).ID, invoiceID, paymentID, input.ExpectedVersion, r.Header.Get("Idempotency-Key"), closePaymentSessionHash(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if op.Replayed {
		s.writePaymentCloseReplay(w, r, op)
		return
	}
	failClose := func(code string) {
		finishCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer stop()
		if finishErr := s.db.FailPaymentClose(finishCtx, op, code); finishErr != nil {
			s.log.Error("payment close failure record failed", "operation_id", op.OperationID, "error", finishErr)
		}
	}
	configuration, configurationErr := s.paymentConfiguration(ctx)
	if configurationErr != nil {
		failClose("provider_unavailable")
		writeError(w, http.StatusConflict, "payment_close_unavailable", "当前支付渠道不支持可信关单，原支付单继续保留")
		return
	}
	gateway, enabled, gatewayErr := s.db.PaymentTransactionGateway(ctx, op.Provider, op.ProviderPaymentID, configuration)
	closeGateway, supported := gateway.(payment.CloseGateway)
	if gatewayErr != nil || !enabled || !supported {
		failClose("provider_unavailable")
		writeError(w, http.StatusConflict, "payment_close_unavailable", "当前支付渠道不支持可信关单，原支付单继续保留")
		return
	}
	result, closeErr := closeGateway.Close(ctx, payment.CloseRequest{ProviderPaymentID: op.ProviderPaymentID, AmountCents: op.AmountCents, OperationID: op.OperationID})
	if closeErr != nil {
		code := "unknown"
		if errors.Is(closeErr, payment.ErrCloseInvalid) {
			code = "provider_invalid"
		} else if errors.Is(closeErr, payment.ErrCloseUnknown) || errors.Is(closeErr, context.DeadlineExceeded) || errors.Is(closeErr, context.Canceled) {
			code = "unknown"
		}
		failClose(code)
		if code == "unknown" {
			writeError(w, http.StatusBadGateway, "payment_close_unknown", "渠道关单结果未知，原支付单继续冻结，请人工核验")
		} else {
			writeError(w, http.StatusBadGateway, "payment_close_failed", "渠道未确认最终关闭，原支付单继续冻结")
		}
		return
	}
	completed, completeErr := s.db.CompletePaymentClose(ctx, op, result, closePaymentSessionHash(r))
	if completeErr != nil {
		failClose("unknown")
		s.fail(w, r, completeErr)
		return
	}
	write(w, http.StatusOK, paymentClosePayload(completed))
}

func (s *Server) writePaymentCloseReplay(w http.ResponseWriter, r *http.Request, op store.PaymentCloseOperation) {
	switch op.Status {
	case "closed":
		write(w, http.StatusOK, paymentClosePayload(op))
	case "unknown":
		if op.ResultCode == "provider_invalid" {
			writeError(w, http.StatusBadGateway, "payment_close_failed", "渠道未确认最终关闭，原支付单继续冻结")
			return
		}
		writeError(w, http.StatusBadGateway, "payment_close_unknown", "渠道关单结果未知，原支付单继续冻结，请人工核验")
	case "checking":
		writeError(w, http.StatusConflict, "payment_close_in_progress", "关单操作处理中，请稍后查询")
	case "succeeded":
		writeError(w, http.StatusConflict, "payment_close_paid", "渠道已报告付款成功，未解除付款保护")
	default:
		writeError(w, http.StatusConflict, "payment_close_failed", "渠道未确认最终关闭，原支付单继续冻结")
	}
}

func (s *Server) paymentCloseStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	invoiceID, ok := pathID(w, r)
	if !ok {
		return
	}
	paymentID, err := strconv.ParseInt(r.PathValue("payment_id"), 10, 64)
	if err != nil || paymentID < 1 {
		writeError(w, 400, "invalid_id", "支付单编号无效")
		return
	}
	var out map[string]any
	var raw []byte
	err = s.db.Pool.QueryRow(r.Context(), `SELECT jsonb_build_object('payment_id',p.id,'invoice_id',i.id,'status',p.status,'version',i.version,
 'operations_total',(SELECT count(*) FROM payment_close_operations c WHERE c.payment_id=p.id),
 'operations',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',c.id,'operation_id',c.operation_id,'status',c.status,'result_code',c.result_code,'actor_id',c.actor_id,'expected_version',c.expected_version,'started_at',c.started_at,'completed_at',c.completed_at) ORDER BY c.id DESC) FROM (SELECT * FROM payment_close_operations WHERE payment_id=p.id ORDER BY id DESC LIMIT 50) c),'[]'::jsonb),
 'anomalies_total',(SELECT count(*) FROM payment_close_anomalies a WHERE a.payment_id=p.id),
 'anomalies',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',a.id,'status',a.status,'amount_cents',a.amount_cents,'external_transaction_id',a.external_transaction_id,'created_at',a.created_at) ORDER BY a.id DESC) FROM (SELECT * FROM payment_close_anomalies WHERE payment_id=p.id ORDER BY id DESC LIMIT 50) a),'[]'::jsonb))
 FROM payment_transactions p JOIN invoices i ON i.id=p.invoice_id AND i.user_id=p.user_id WHERE p.id=$1 AND p.invoice_id=$2 AND admin_customer_allowed($3,p.user_id)`, paymentID, invoiceID, identityFrom(r).ID).Scan(&raw)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		s.fail(w, r, err)
		return
	}
	var provider, reference string
	if err = s.db.Pool.QueryRow(r.Context(), `SELECT provider,provider_payment_id FROM payment_transactions WHERE id=$1`, paymentID).Scan(&provider, &reference); err != nil {
		s.fail(w, r, err)
		return
	}
	cfg, cfgErr := s.paymentConfiguration(r.Context())
	g, enabled, gErr := s.db.PaymentTransactionGateway(r.Context(), provider, reference, cfg)
	_, supported := g.(payment.CloseGateway)
	out["close_supported"] = cfgErr == nil && gErr == nil && enabled && supported
	write(w, 200, out)
}
