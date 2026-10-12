package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type OrderSettings struct {
	LegacyRequireCertification bool      `json:"legacy_require_certification"`
	CertificationEnabled       bool      `json:"certification_enabled"`
	RequireVerifiedPhone       bool      `json:"require_verified_phone"`
	RequireCertification       bool      `json:"require_certification"`
	Version                    int64     `json:"version"`
	UpdatedAt                  time.Time `json:"updated_at"`
}
type OrderSettingsInput struct {
	RequireVerifiedPhone *bool `json:"require_verified_phone"`
	RequireCertification *bool `json:"require_certification"`
	ExpectedVersion      int64 `json:"expected_version"`
}

const orderSettingsColumns = "require_verified_phone,require_certification,version,updated_at"

func scanOrderSettings(row pgx.Row) (out OrderSettings, err error) {
	err = row.Scan(&out.RequireVerifiedPhone, &out.RequireCertification, &out.Version, &out.UpdatedAt)
	return
}
func (s *Store) OrderSettings(ctx context.Context) (OrderSettings, error) {
	out, err := scanOrderSettings(s.Pool.QueryRow(ctx, "SELECT "+orderSettingsColumns+" FROM order_settings WHERE id=1"))
	if err != nil {
		return out, err
	}
	err = s.Pool.QueryRow(ctx, "SELECT require_for_purchase,enabled FROM certification_settings WHERE id=1").Scan(&out.LegacyRequireCertification, &out.CertificationEnabled)
	return out, err
}
func (s *Store) UpdateOrderSettings(ctx context.Context, actor int64, sessionHash, key string, in OrderSettingsInput) (OrderSettings, error) {
	var out OrderSettings
	if in.RequireVerifiedPhone == nil || in.RequireCertification == nil || in.ExpectedVersion < 1 || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_order_settings", "请提供完整订购设置、有效版本及幂等键")
	}
	tx, err := settingsManagementTx(ctx, s, actor)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = recheckSettingsManager(ctx, tx, actor, sessionHash); err != nil {
		return out, err
	}
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	var saved string
	var receipt []byte
	err = tx.QueryRow(ctx, "SELECT request_hash,result FROM order_settings_receipts WHERE actor_id=$1 AND idempotency_key=$2", actor, key).Scan(&saved, &receipt)
	if err == nil {
		if saved != hash {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同设置")
		}
		if err = json.Unmarshal(receipt, &out); err != nil {
			return out, err
		}
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	before, err := scanOrderSettings(tx.QueryRow(ctx, "SELECT "+orderSettingsColumns+" FROM order_settings WHERE id=1 FOR UPDATE"))
	if err != nil {
		return out, err
	}
	if before.Version != in.ExpectedVersion {
		return out, conflict("version_conflict", "订购设置已变更，请重新加载")
	}
	out, err = scanOrderSettings(tx.QueryRow(ctx, "UPDATE order_settings SET require_verified_phone=$1,require_certification=$2,version=version+1,updated_at=now() WHERE id=1 RETURNING "+orderSettingsColumns, *in.RequireVerifiedPhone, *in.RequireCertification))
	if err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actor, "order.settings_updated", "order_settings", 1, map[string]any{"before": before, "after": out}); err != nil {
		return out, err
	}
	raw, _ = json.Marshal(out)
	if _, err = tx.Exec(ctx, "INSERT INTO order_settings_receipts(actor_id,idempotency_key,request_hash,result) VALUES($1,$2,$3,$4)", actor, key, hash, raw); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// CheckOrderAdmission uses canonical account bindings and the latest reviewed
// submission, never editable customer profile/contact fields. Existing order
// receipts are replayed before this check by both creation paths.
func (s *Store) CheckOrderAdmission(ctx context.Context, tx pgx.Tx, userID, productID int64, lock bool) error {
	suffix := ""
	if lock {
		suffix = " FOR SHARE"
	}
	var globalPhone, globalCert, phone, cert bool
	var certEnabled bool
	if err := tx.QueryRow(ctx, "SELECT require_verified_phone,require_certification FROM order_settings WHERE id=1"+suffix).Scan(&globalPhone, &globalCert); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, "SELECT require_verified_phone,require_certification FROM products WHERE id=$1"+suffix, productID).Scan(&phone, &cert); err != nil {
		return err
	}
	var legacyCert bool
	if err := tx.QueryRow(ctx, "SELECT require_for_purchase,enabled FROM certification_settings WHERE id=1"+suffix).Scan(&legacyCert, &certEnabled); err != nil {
		return err
	}
	globalCert = globalCert || legacyCert
	if !globalPhone && !globalCert && !phone && !cert {
		return nil
	}
	var bound bool
	if err := tx.QueryRow(ctx, "SELECT phone_verified AND phone<>'' FROM users WHERE id=$1"+suffix, userID).Scan(&bound); err != nil {
		return err
	}
	if (globalPhone || phone) && !bound {
		return conflict("order_phone_required", "订购前请验证绑定手机，请前往 /console/account")
	}
	if globalCert || cert {
		var profileID int64
		var status, kind string
		err := tx.QueryRow(ctx, "SELECT id,status,certification_type FROM certification_profiles WHERE user_id=$1"+suffix, userID).Scan(&profileID, &status, &kind)
		if errors.Is(err, pgx.ErrNoRows) {
			if !certEnabled {
				return conflict("order_certification_unavailable", "订购要求实名认证，但认证入口已关闭，请联系管理员启用实名认证")
			}
			return conflict("order_certification_required", "订购前请完成实名认证，请前往 /console/certification")
		}
		if err != nil {
			return err
		}
		var valid bool
		var sealed []byte
		var aad string
		err = tx.QueryRow(ctx, `SELECT status='approved' AND certification_type=$2 AND reviewed_at IS NOT NULL,payload_encrypted,payload_aad FROM certification_submissions WHERE profile_id=$1 AND user_id=$3 ORDER BY id DESC LIMIT 1`+suffix, profileID, kind, userID).Scan(&valid, &sealed, &aad)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if valid {
			payload, e := openCertification(s.ProvisionEncryptionKey, aad, sealed)
			valid = e == nil && validateCertificationPayload(kind, payload) == nil
		}

		if status != "approved" || !valid {
			var enabled bool
			if err := tx.QueryRow(ctx, "SELECT enabled FROM certification_settings WHERE id=1").Scan(&enabled); err != nil {
				return err
			}
			if !enabled {
				return conflict("order_certification_unavailable", "订购要求实名认证，但认证入口已关闭，请联系管理员启用实名认证")
			}
			return conflict("order_certification_required", "订购前请完成审核通过的实名认证，请前往 /console/certification")
		}
	}
	return nil
}
