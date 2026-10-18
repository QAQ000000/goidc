package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/codex/zjmfgo/internal/payment"
)

func (s *Server) reconcilePayment(w http.ResponseWriter, r *http.Request) {
	invoiceID, ok := pathID(w, r)
	if !ok {
		return
	}
	paymentID, err := strconv.ParseInt(r.PathValue("payment_id"), 10, 64)
	if err != nil || paymentID < 1 {
		writeError(w, 400, "invalid_id", "支付单编号无效")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	check, err := s.db.BeginPaymentCheck(ctx, identityFrom(r).ID, invoiceID, paymentID, adminRoute(r))
	if err != nil {
		w.Header().Set("Retry-After", "30")
		s.fail(w, r, err)
		return
	}
	failCheck := func(code string) {
		finishCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer stop()
		if err := s.db.FailPaymentCheck(finishCtx, check, code); err != nil {
			s.log.Error("payment query failure record failed", "check_id", check.ID, "error", err)
		}
	}
	gateway, enabled, gatewayErr := s.db.PaymentTransactionGateway(ctx, check.Provider, check.ProviderPaymentID, s.paymentFallback())
	queryGateway, supported := gateway.(payment.QueryGateway)
	if gatewayErr != nil || !enabled || !supported {
		failCheck("provider_unavailable")
		writeError(w, 409, "payment_query_unavailable", "当前支付渠道未启用查询")
		return
	}
	result, err := queryGateway.Query(ctx, payment.QueryRequest{ProviderPaymentID: check.ProviderPaymentID, AmountCents: check.AmountCents})
	if err != nil {
		code := "provider_unavailable"
		if errors.Is(err, payment.ErrQueryInvalid) {
			code = "provider_invalid"
		}
		failCheck(code)
		writeError(w, 502, "payment_query_failed", "支付渠道查询未确认结果，请稍后重试；原支付单继续保留")
		return
	}
	out, err := s.db.CompletePaymentCheck(ctx, check, result)
	if err != nil {
		failCheck("settlement_failed")
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
