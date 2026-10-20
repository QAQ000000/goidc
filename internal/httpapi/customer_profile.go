package httpapi

import (
	"context"
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

type customerProfileInput = store.CustomerProfileInput
type customDef struct {
	typ, pattern string
	choices      []string
	required     bool
}

func validateCustomFieldValues(allowed map[string]customDef, values, previous map[string]any) (map[string]any, error) {
	definitions := make(map[string]store.CustomerCustomFieldDefinition, len(allowed))
	for key, def := range allowed {
		definitions[key] = store.CustomerCustomFieldDefinition{Type: def.typ, Pattern: def.pattern, Choices: def.choices, Required: def.required}
	}
	return store.ValidateCustomerCustomFieldValues(definitions, values, previous)
}
func customerProfileFailure(w http.ResponseWriter, r *http.Request, err error) bool {
	if err != nil && err.Error() == "invalid_custom_field" {
		writeError(w, http.StatusBadRequest, "invalid_custom_field", "客户自定义字段值无效")
		return true
	}
	if err != nil && err.Error() == "stale_customer_profile" {
		writeError(w, http.StatusConflict, "stale_customer_profile", "客户资料已更新，请刷新后重试")
		return true
	}
	return false
}

// Serialize permission checks with staff/role mutations, then verify the request's credential.
func lockCustomerManager(ctx context.Context, tx pgx.Tx, actor int64) (super, group, sales bool, err error) {
	return store.LockCustomerManager(ctx, tx, actor)
}
