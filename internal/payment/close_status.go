package payment

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
)

// LookupClose reads evidence for exactly one prior operation. It must never
// send Close, or treat an unpaid/not-found response as a final close.
type CloseStatusRequest struct {
	OperationID, ProviderPaymentID string
	AmountCents                    int64
}
type CloseStatusResult struct {
	OperationID           string `json:"operation_id"`
	ProviderPaymentID     string `json:"provider_payment_id"`
	AmountCents           int64  `json:"amount_cents"`
	Status                string `json:"status"`
	ExternalTransactionID string `json:"external_transaction_id"`
	ProviderEvidenceID    string `json:"provider_evidence_id"`
}
type CloseStatusGateway interface {
	Gateway
	LookupClose(context.Context, CloseStatusRequest) (CloseStatusResult, error)
}

// Explicit simulation only. No evidence survives process restart, so lookup
// after restart returns unknown. The bounded cache can discard evidence safely.
var mockCloseEvidence = struct {
	sync.Mutex
	values map[string]CloseResult
}{values: make(map[string]CloseResult)}

func (g MockGateway) closeEvidenceKey(req CloseStatusRequest) string {
	return fmt.Sprintf("%x:%s:%s:%d", sha256.Sum256(g.Secret), req.OperationID, req.ProviderPaymentID, req.AmountCents)
}
func (g MockGateway) recordCloseEvidence(result CloseResult) {
	mockCloseEvidence.Lock()
	defer mockCloseEvidence.Unlock()
	if len(mockCloseEvidence.values) >= 4096 {
		for key := range mockCloseEvidence.values {
			delete(mockCloseEvidence.values, key)
			break
		}
	}
	mockCloseEvidence.values[g.closeEvidenceKey(CloseStatusRequest{result.OperationID, result.ProviderPaymentID, result.AmountCents})] = result
}
func (g MockGateway) LookupClose(ctx context.Context, req CloseStatusRequest) (CloseStatusResult, error) {
	if err := ctx.Err(); err != nil {
		return CloseStatusResult{}, err
	}
	if req.OperationID == "" || req.ProviderPaymentID == "" || req.AmountCents < 1 {
		return CloseStatusResult{}, ErrCloseInvalid
	}
	out := CloseStatusResult{OperationID: req.OperationID, ProviderPaymentID: req.ProviderPaymentID, AmountCents: req.AmountCents, Status: "unknown"}
	mockCloseEvidence.Lock()
	saved, ok := mockCloseEvidence.values[g.closeEvidenceKey(req)]
	mockCloseEvidence.Unlock()
	if ok {
		out.Status = saved.Status
		out.ExternalTransactionID = saved.ExternalTransactionID
		out.ProviderEvidenceID = "mock:" + saved.OperationID
	}
	return out, nil
}
