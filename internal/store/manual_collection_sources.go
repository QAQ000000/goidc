package store

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const ManualCollectionProvider = "manual_offline"

func manualCollectionReference(method, account, transaction string) string {
	scope := sha256.Sum256([]byte(method + "\x00" + account))
	receipt := sha256.Sum256([]byte(transaction))
	return "manual:" + hex.EncodeToString(scope[:]) + ":" + hex.EncodeToString(receipt[:])
}

// Manual collections have immutable offline account identity, not a gateway
// credential or configurable channel. Resolving them never creates a gateway.
func ManualCollectionMerchantScope(reference string, sealed []byte) (string, error) {
	parts := strings.Split(reference, ":")
	if len(sealed) != 0 || len(parts) != 3 || parts[0] != "manual" || !validRenewalBatchID(parts[1]) || !validRenewalBatchID(parts[2]) {
		return "", conflict("external_refund_merchant_unavailable", "人工收款来源身份不可核验")
	}
	return "manual:" + parts[1], nil
}
