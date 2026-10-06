package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
)

// Domain separation binds supplier passwords and fixed custom fields to their owners.
func supplierSeal(key []byte, domain string, id int64, value any) ([]byte, error) {
	if len(key) != 32 {
		return nil, conflict("supplier_encryption_unconfigured", "供应商凭据加密密钥未配置")
	}
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	n := make([]byte, g.NonceSize())
	if _, e = rand.Read(n); e != nil {
		return nil, e
	}
	raw, e := json.Marshal(value)
	if e != nil {
		return nil, e
	}
	return g.Seal(n, n, raw, []byte(fmt.Sprintf("supplier:%s:%d", domain, id))), nil
}
func supplierUnseal(key []byte, domain string, id int64, sealed []byte, value any) error {
	if len(key) != 32 {
		return conflict("supplier_encryption_unconfigured", "供应商凭据加密密钥未配置")
	}
	b, e := aes.NewCipher(key)
	if e != nil {
		return e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return e
	}
	if len(sealed) < g.NonceSize() {
		return conflict("supplier_credentials_invalid", "供应商加密配置不可用")
	}
	raw, e := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], []byte(fmt.Sprintf("supplier:%s:%d", domain, id)))
	if e != nil || json.Unmarshal(raw, value) != nil {
		return conflict("supplier_credentials_invalid", "供应商加密配置不可用")
	}
	return nil
}
