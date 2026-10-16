package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// These limits apply to the complete public catalogue, never a truncated subset.
const registrationFieldLimit = 100
const registrationFieldBytes = 256 * 1024

type registrationField struct {
	Key         string   `json:"field_key"`
	Label       string   `json:"label"`
	Type        string   `json:"field_type"`
	Description string   `json:"description"`
	Required    bool     `json:"required"`
	Choices     []string `json:"choices"`
}
type registrationDefinition struct {
	Field   registrationField
	Pattern string
}
type registrationSchema struct {
	Items   []registrationField `json:"items"`
	Version string              `json:"definitions_version"`
}

// Lock order is global authorization -> catalogue -> site/auth settings. CRUD
// takes the same global lock and its statement trigger updates catalogue first.
func registrationFieldsTx(ctx context.Context, tx pgx.Tx) (registrationSchema, map[string]customDef, error) {
	var out registrationSchema
	out.Items = []registrationField{}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return out, nil, err
	}
	var version int64
	if err := tx.QueryRow(ctx, "SELECT version FROM customer_field_catalogue WHERE id=1 FOR SHARE").Scan(&version); err != nil {
		return out, nil, err
	}
	enabled, err := registrationEnabledTx(ctx, tx)
	if err != nil {
		return out, nil, err
	}
	if !enabled {
		return out, nil, errRegistrationDisabled
	}
	rows, err := tx.Query(ctx, `SELECT field_key,label,field_type,description,required,choices,validation_pattern FROM customer_custom_fields WHERE active AND show_on_register AND NOT admin_only ORDER BY sort_order,id LIMIT $1`, registrationFieldLimit+1)
	if err != nil {
		return out, nil, err
	}
	defer rows.Close()
	defs := []registrationDefinition{}
	allowed := map[string]customDef{}
	size := 0
	for rows.Next() {
		var d registrationDefinition
		if err = rows.Scan(&d.Field.Key, &d.Field.Label, &d.Field.Type, &d.Field.Description, &d.Field.Required, &d.Field.Choices, &d.Pattern); err != nil {
			return out, nil, err
		}
		if d.Field.Choices == nil {
			d.Field.Choices = []string{}
		}
		encoded, e := json.Marshal(d)
		if e != nil {
			return out, nil, e
		}
		size += len(encoded)
		if len(defs) >= registrationFieldLimit || size > registrationFieldBytes {
			return out, nil, errRegistrationCatalogueLimit
		}
		defs = append(defs, d)
		out.Items = append(out.Items, d.Field)
		allowed[d.Field.Key] = customDef{typ: d.Field.Type, pattern: d.Pattern, choices: d.Field.Choices, required: d.Field.Required}
	}
	if err = rows.Err(); err != nil {
		return out, nil, err
	}
	raw, err := json.Marshal(defs)
	if err != nil {
		return out, nil, err
	}
	sum := sha256.Sum256(raw)
	out.Version = hex.EncodeToString(sum[:])
	return out, allowed, nil
}

func registrationEnabledTx(ctx context.Context, tx pgx.Tx) (bool, error) {
	var siteEnabled, emailEnabled bool
	if err := tx.QueryRow(ctx, "SELECT registration_enabled FROM site_settings WHERE id=1 FOR SHARE").Scan(&siteEnabled); err != nil {
		return false, err
	}
	if err := tx.QueryRow(ctx, "SELECT registration_email_enabled FROM auth_settings WHERE id=1 FOR SHARE").Scan(&emailEnabled); err != nil {
		return false, err
	}
	return siteEnabled && emailEnabled, nil
}

func (s *Server) registrationFields(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	schema, _, err := registrationFieldsTx(r.Context(), tx)
	if err != nil {
		s.registrationFieldsError(w, r, err)
		return
	}
	enabled, err := registrationEnabledTx(r.Context(), tx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !enabled {
		writeError(w, 403, "registration_disabled", "本站暂未开放注册")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, schema)
}

var errRegistrationDisabled = errors.New("registration disabled")
var errRegistrationCatalogueLimit = errors.New("registration catalogue exceeds bounded response")

func (s *Server) registrationFieldsError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errRegistrationDisabled) {
		writeError(w, 403, "registration_disabled", "本站暂未开放注册")
		return
	}
	if errors.Is(err, errRegistrationCatalogueLimit) {
		writeError(w, 503, "registration_fields_unavailable", "注册字段暂不可用，请联系管理员")
		return
	}
	s.fail(w, r, err)
}
