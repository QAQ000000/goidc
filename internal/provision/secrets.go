package provision

import (
	"encoding/json"
	"fmt"

	"github.com/codex/zjmfgo/internal/secure"
)

func Seal(key []byte, serviceID int64, value Credentials) ([]byte, error) {
	return sealAssociated(key, []byte(fmt.Sprintf("nokvm-service:%d", serviceID)), value)
}

func SealJobCredentials(key []byte, serviceID int64, jobKey string, value Credentials) ([]byte, error) {
	return sealAssociated(key, []byte(fmt.Sprintf("nokvm-password-job:%d:%s", serviceID, jobKey)), value)
}

func sealAssociated(key, associated []byte, value Credentials) ([]byte, error) {
	if len(key) != 32 {
		return nil, &Error{Code: "encryption_unconfigured"}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return secure.Seal(key, associated, raw)
}
func Unseal(key []byte, serviceID int64, sealed []byte) (Credentials, error) {
	return unsealAssociated(key, []byte(fmt.Sprintf("nokvm-service:%d", serviceID)), sealed)
}

func UnsealJobCredentials(key []byte, serviceID int64, jobKey string, sealed []byte) (Credentials, error) {
	return unsealAssociated(key, []byte(fmt.Sprintf("nokvm-password-job:%d:%s", serviceID, jobKey)), sealed)
}

func unsealAssociated(key, associated, sealed []byte) (Credentials, error) {
	var value Credentials
	if len(key) != 32 {
		return value, &Error{Code: "encryption_unconfigured"}
	}
	raw, err := secure.Open(key, associated, sealed)
	if err != nil || json.Unmarshal(raw, &value) != nil {
		return value, &Error{Code: "invalid_credentials"}
	}
	return value, nil
}
