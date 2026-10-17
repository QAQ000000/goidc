package store

import (
	"encoding/json"
	"fmt"

	"github.com/codex/zjmfgo/internal/secure"
)

// Domain separation binds supplier passwords and fixed custom fields to their owners.
func supplierSeal(key []byte, domain string, id int64, value any) ([]byte, error) {
	if len(key) != 32 {
		return nil, conflict("supplier_encryption_unconfigured", "供应商凭据加密密钥未配置")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return secure.Seal(key, []byte(fmt.Sprintf("supplier:%s:%d", domain, id)), raw)
}
func supplierUnseal(key []byte, domain string, id int64, sealed []byte, value any) error {
	if len(key) != 32 {
		return conflict("supplier_encryption_unconfigured", "供应商凭据加密密钥未配置")
	}
	raw, err := secure.Open(key, []byte(fmt.Sprintf("supplier:%s:%d", domain, id)), sealed)
	if err != nil || json.Unmarshal(raw, value) != nil {
		return conflict("supplier_credentials_invalid", "供应商加密配置不可用")
	}
	return nil
}
