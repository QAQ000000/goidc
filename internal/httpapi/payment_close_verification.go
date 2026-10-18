package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/codex/zjmfgo/internal/store"
)

func paymentVerificationIDs(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	invoice, ok := pathID(w, r)
	if !ok {
		return 0, 0, false
	}
	pid, err := strconv.ParseInt(r.PathValue("payment_id"), 10, 64)
	if err != nil || pid < 1 {
		writeError(w, 400, "invalid_id", "支付编号无效")
		return 0, 0, false
	}
	return invoice, pid, true
}
func (s *Server) closeVerificationList(w http.ResponseWriter, r *http.Request) {
	invoice, pid, ok := paymentVerificationIDs(w, r)
	if !ok {
		return
	}
	limit, offset := 20, 0
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeError(w, 400, "invalid_pagination", "分页参数无效")
			return
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil {
			writeError(w, 400, "invalid_pagination", "分页参数无效")
			return
		}
	}
	out, err := s.db.ListPaymentCloseVerifications(r.Context(), identityFrom(r).ID, invoice, pid, closePaymentSessionHash(r), limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
func (s *Server) closeVerificationWrite(w http.ResponseWriter, r *http.Request) {
	invoice, pid, ok := paymentVerificationIDs(w, r)
	if !ok {
		return
	}
	var input struct {
		CloseOperationID int64 `json:"close_operation_id"`
		ExpectedVersion  int64 `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	v, err := s.db.BeginPaymentCloseVerification(ctx, identityFrom(r).ID, invoice, pid, input.CloseOperationID, input.ExpectedVersion, r.Header.Get("Idempotency-Key"), closePaymentSessionHash(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if v.Replayed {
		if v.Status == "expired" && v.ResultStatus == "" {
			// Only a read-only lookup reservation may be retired after rereading
			// its expired empty result. Never infer the underlying Close outcome.
			writeError(w, 409, "close_verification_expired", "原只读核验请求已过期且无可信结果，请读取原记录后重新发起核验；原支付继续冻结")
			return
		}
		s.writeCloseVerification(w, r, v)
		return
	}
	var result payment.CloseStatusResult
	code := "verified"
	result.Status = "unsupported"
	gateway, gErr := s.db.PaymentCloseVerificationGateway(ctx, v, s.paymentFallback())
	if gErr != nil {
		result.Status = "error"
		code = "credentials_unavailable"
	} else if lookup, supported := gateway.(payment.CloseStatusGateway); !supported {
		code = "provider_unavailable"
	} else {
		result, err = lookup.LookupClose(ctx, payment.CloseStatusRequest{OperationID: v.OperationID, ProviderPaymentID: v.ProviderPaymentID, AmountCents: v.AmountCents})
		if err != nil {
			// No response is not a stored business result. Leave the immutable request
			// in flight/expired and preserve the browser's original recovery key.
			if !errors.Is(err, payment.ErrCloseInvalid) {
				writeError(w, 502, "close_verification_transport_unknown", "渠道核验未收到可信回执，请保留请求并读取原记录")
				return
			}
			result = payment.CloseStatusResult{Status: "error"}
			code = "provider_invalid"
		}
	}
	if result.Status == "closed" || result.Status == "succeeded" || result.Status == "unknown" {
		if result.OperationID != v.OperationID || result.ProviderPaymentID != v.ProviderPaymentID || result.AmountCents != v.AmountCents || len(result.ExternalTransactionID) > 160 || len(result.ProviderEvidenceID) > 160 || (result.Status == "succeeded" && strings.TrimSpace(result.ExternalTransactionID) == "") {
			result = payment.CloseStatusResult{Status: "error"}
			code = "provider_invalid"
		}
	} else if result.Status != "unsupported" && result.Status != "error" {
		result = payment.CloseStatusResult{Status: "error"}
		code = "provider_invalid"
	}
	completed, err := s.db.CompletePaymentCloseVerification(ctx, v, result, code, closePaymentSessionHash(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeCloseVerification(w, r, completed)
}
func (s *Server) closeVerificationReview(w http.ResponseWriter, r *http.Request) {
	invoice, pid, ok := paymentVerificationIDs(w, r)
	if !ok {
		return
	}
	if r.PathValue("action") != "confirm" && r.PathValue("action") != "reject" {
		writeError(w, 404, "not_found", "复核动作不存在")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("verification_id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "invalid_id", "核验编号无效")
		return
	}
	var in struct {
		ExpectedVersion int64  `json:"expected_version"`
		Note            string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := s.db.ReviewPaymentCloseVerification(r.Context(), identityFrom(r).ID, invoice, pid, id, in.ExpectedVersion, r.PathValue("action") == "confirm", in.Note, r.Header.Get("Idempotency-Key"), closePaymentSessionHash(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeCloseVerification(w, r, v)
}
func (s *Server) writeCloseVerification(w http.ResponseWriter, r *http.Request, v store.PaymentCloseVerification) {
	if identityFrom(r).ID != v.RequestedBy {
		v.RequestKeySHA256 = ""
	}
	resolution, err := s.db.PaymentCloseVerificationResolution(r.Context(), v.PaymentID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	code := 201
	if v.Replayed {
		code = 200
	}
	if v.Status == "checking" {
		code = 202
	}
	write(w, code, map[string]any{"verification": v, "resolution": resolution})
}
