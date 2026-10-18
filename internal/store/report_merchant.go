package store

// ReportPaymentMerchantScope derives the same immutable merchant identity used
// by refund verification from a payment's original encrypted credential snapshot.
// Reports must treat an error as unverified rather than assuming global
// uniqueness of an external transaction number.
func (s *Store) ReportPaymentMerchantScope(provider, reference string, sealed []byte) (string, error) {
	return s.externalRefundMerchantScope(provider, reference, sealed)
}
