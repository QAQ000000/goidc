package supplier

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/codex/zjmfgo/internal/channel"
)

// Lookup resolves durable supplier provider IDs to their compiled descriptor.
// It reads no account rows and never constructs an adapter.
func Lookup(provider string) (Definition, bool) {
	providerState.RLock()
	metadata, ok := providerState.metadata[provider]
	id := providerState.descriptor[provider]
	metadata.IdentityFields = append([]string(nil), metadata.IdentityFields...)
	providerState.RUnlock()
	if !ok {
		return Definition{}, false
	}
	desc, ok := channel.Lookup(channel.Provision, id)
	return Definition{Provider: metadata, Descriptor: desc}, ok
}

// NormalizeAccount checks only public configuration. Credential presence and
// factory construction belong to New; account management must remain local.
func NormalizeAccount(account Account) (Account, error) {
	definition, ok := Lookup(account.Provider)
	if !ok {
		return Account{}, &Error{Code: "unsupported_provider"}
	}
	if account.SchemaVersion != 0 && account.SchemaVersion != definition.Descriptor.SchemaVersion {
		return Account{}, &Error{Code: "schema_incompatible"}
	}
	values := make(map[string]any, len(account.Values))
	for key, value := range account.Values {
		values[key] = value
	}
	secrets := make(map[string]string, len(account.Secrets))
	for key, value := range account.Secrets {
		secrets[key] = value
	}
	// Legacy fields are aliases owned by this adapter, not a universal account
	// schema. Other providers need neither an HTTP endpoint nor a username.
	if account.Provider == ProviderZJMF {
		for key, legacy := range map[string]string{"endpoint": account.BaseURL, "username": account.Username} {
			if value, exists := values[key]; exists {
				if legacy != "" && value != legacy {
					return Account{}, &Error{Code: "ambiguous_configuration"}
				}
			} else if legacy != "" {
				values[key] = legacy
			}
		}
		if account.Password != "" {
			if secret, exists := secrets["secret"]; exists && secret != account.Password {
				return Account{}, &Error{Code: "ambiguous_configuration"}
			}
			secrets["secret"] = account.Password
		}
		if endpoint, ok := values["endpoint"].(string); ok {
			values["endpoint"] = strings.TrimRight(strings.TrimSpace(endpoint), "/")
		}
		if username, ok := values["username"].(string); ok {
			values["username"] = strings.TrimSpace(username)
		}
	} else if account.BaseURL != "" || account.Username != "" || account.Password != "" {
		return Account{}, &Error{Code: "ambiguous_configuration"}
	}
	values, errs := channel.NormalizeConfig(definition.Descriptor, values)
	if len(errs) != 0 {
		return Account{}, &Error{Code: "invalid_configuration"}
	}
	if normalize := definition.Provider.NormalizeConfig; normalize != nil {
		canonical, err := normalize(values)
		if err != nil {
			return Account{}, &Error{Code: "invalid_configuration"}
		}
		values, errs = channel.NormalizeConfig(definition.Descriptor, canonical)
		if len(errs) != 0 {
			return Account{}, &Error{Code: "invalid_configuration"}
		}
	}
	account.Values, account.Secrets, account.SchemaVersion = values, secrets, definition.Descriptor.SchemaVersion
	if account.Provider == ProviderZJMF {
		account.BaseURL, _ = values["endpoint"].(string)
		account.Username, _ = values["username"].(string)
		account.Password = secrets["secret"]
		endpoint, err := zjmfBaseURL(account.BaseURL)
		if err != nil {
			return Account{}, err
		}
		account.BaseURL = endpoint.String()
		account.Values["endpoint"] = account.BaseURL
	}
	return account, nil
}

// AccountIdentity cannot depend on credentials, schema versions or optional
// settings: parallel rows for one upstream account would bypass its job lease.
func AccountIdentity(account Account) (string, error) {
	account, err := NormalizeAccount(account)
	if err != nil {
		return "", err
	}
	definition, _ := Lookup(account.Provider)
	if len(definition.Provider.IdentityFields) == 0 {
		return "", &Error{Code: "account_identity_unavailable"}
	}
	identity := make(map[string]any, len(definition.Provider.IdentityFields))
	for _, field := range definition.Provider.IdentityFields {
		value, ok := account.Values[field]
		if !ok {
			return "", &Error{Code: "account_identity_unavailable"}
		}
		identity[field] = value
	}
	raw, err := json.Marshal(struct {
		Provider string         `json:"provider"`
		Values   map[string]any `json:"identity"`
	}{account.Provider, identity})
	if err != nil {
		return "", &Error{Code: "invalid_configuration"}
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
