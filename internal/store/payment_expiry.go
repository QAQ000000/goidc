package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/codex/zjmfgo/internal/secure"
	"github.com/jackc/pgx/v5"
)

type paymentConfigurationKey struct{}
type paymentChannelInstanceKey struct{}

func WithPaymentConfiguration(ctx context.Context, configuration PaymentConfiguration) context.Context {
	if configuration.Runtime != nil {
		ctx = WithPaymentChannelInstance(ctx, configuration.Runtime.InstanceID)
	}
	return context.WithValue(WithPaymentConfigurationVersion(ctx, configuration.Version), paymentConfigurationKey{}, configuration)
}

func WithPaymentChannelInstance(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, paymentChannelInstanceKey{}, strings.TrimSpace(id))
}

func paymentChannelInstance(ctx context.Context) string {
	id, _ := ctx.Value(paymentChannelInstanceKey{}).(string)
	return strings.TrimSpace(id)
}

func (s *Store) paymentCredentials(reference string, raw []byte, seal bool) ([]byte, error) {
	if len(s.ProvisionEncryptionKey) != 32 {
		return nil, conflict("payment_encryption_unconfigured", "支付密钥加密尚未配置")
	}
	aadBytes := []byte("zjmfgo:payment-credentials:v1:" + reference)
	if seal {
		return secure.Seal(s.ProvisionEncryptionKey, aadBytes, raw)
	}
	out, err := secure.Open(s.ProvisionEncryptionKey, aadBytes, raw)
	if err != nil {
		return nil, fmt.Errorf("payment credentials unavailable")
	}
	return out, err
}

func (s *Store) snapshotPaymentCredentials(reference string, cfg PaymentConfiguration, legacy bool) ([]byte, error) {
	cfg.Configured = true
	if _, ok := cfg.Gateway("epay_alipay", true); !ok {
		return nil, conflict("payment_credentials_unavailable", "原支付商户凭据不可用，请先恢复原配置")
	}
	// Existing intents remain queryable after their channel is disabled.
	identity := payment.EpayConfig{APIURL: cfg.APIURL, PID: cfg.PID, Key: cfg.Key, PublicOrigin: cfg.PublicOrigin, Types: []string{"alipay", "wxpay", "qqpay", "bank"}}
	if !legacy {
		identity.TransactionNamespace = payment.EpayMerchantNamespace(cfg.APIURL, cfg.PID)
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		return nil, err
	}
	return s.paymentCredentials(reference, raw, true)
}

func (s *Store) PaymentTransactionGateway(ctx context.Context, provider, reference string, fallback PaymentConfiguration) (payment.Gateway, bool, error) {
	return s.PaymentTransactionGatewayTx(ctx, nil, provider, reference, fallback)
}

// PaymentTransactionGatewayTx resolves a transaction snapshot through the
// supplied transaction, avoiding a second pool checkout for transaction-scoped
// callers such as invoice detail rendering.
func (s *Store) PaymentTransactionGatewayTx(ctx context.Context, tx pgx.Tx, provider, reference string, fallback PaymentConfiguration) (payment.Gateway, bool, error) {
	var sealed []byte
	var instanceID string
	var err error
	if tx != nil {
		err = tx.QueryRow(ctx, "SELECT credential_snapshot,coalesce(channel_instance_id,'') FROM payment_transactions WHERE provider=$1 AND provider_payment_id=$2", provider, reference).Scan(&sealed, &instanceID)
	} else {
		err = s.Pool.QueryRow(ctx, "SELECT credential_snapshot,coalesce(channel_instance_id,'') FROM payment_transactions WHERE provider=$1 AND provider_payment_id=$2", provider, reference).Scan(&sealed, &instanceID)
	}
	if err != nil {
		return nil, false, err
	}
	if len(sealed) == 0 {
		// Only the pre-snapshot compatibility protocols may consult current
		// settings. A new module must never borrow a replacement merchant.
		owner, known := payment.MethodOwner(provider)
		if !known || (owner != "mock" && owner != "epay") || (instanceID != "" && instanceID != "payment-default" && instanceID != "payment-mock-default") {
			return nil, false, nil
		}
		configuration, err := s.PaymentConfigurationTx(ctx, tx, fallback)
		if err != nil {
			return nil, false, err
		}
		gateway, ok := configuration.Gateway(provider, true)
		return gateway, ok, nil
	}
	raw, err := s.paymentCredentials(reference, sealed, false)
	if err != nil {
		return nil, false, err
	}
	var cfg struct {
		payment.EpayConfig
		SnapshotVersion int    `json:"snapshot_version"`
		Unavailable     bool   `json:"unavailable"`
		Implementation  string `json:"implementation"`
		MockSecret      string `json:"mock_secret"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return nil, false, fmt.Errorf("payment credentials unavailable")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, false, fmt.Errorf("payment credentials unavailable")
	}
	if _, versioned := fields["snapshot_version"]; versioned {
		return runtimePaymentGateway(raw, provider, instanceID)
	}
	if cfg.Unavailable {
		return nil, false, nil
	}
	if provider == "mock" {
		if cfg.Implementation != "mock" || cfg.MockSecret == "" {
			return nil, false, fmt.Errorf("payment credentials unavailable")
		}
		gateway, ok := payment.For(provider, cfg.MockSecret)
		return gateway, ok, nil
	}
	gateway, ok := payment.For(provider, fallback.MockSecret, cfg.EpayConfig)
	return gateway, ok, nil
}

// Called with the configuration lock held before changing merchant identity.
// Legacy transactions need the previous merchant preserved before rotation.
func (s *Store) preserveLegacyPaymentCredentials(ctx context.Context, tx pgx.Tx, previous PaymentConfiguration) error {
	rows, err := tx.Query(ctx, "SELECT id,provider_payment_id,status FROM payment_transactions WHERE provider LIKE 'epay_%' AND octet_length(credential_snapshot)=0 FOR UPDATE")
	if err != nil {
		return err
	}
	type target struct {
		id        int64
		reference string
		status    string
	}
	var targets []target
	for rows.Next() {
		var item target
		if err = rows.Scan(&item.id, &item.reference, &item.status); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range targets {
		sealed, sealErr := s.snapshotPaymentCredentials(item.reference, previous, true)
		if sealErr != nil {
			var unavailable *Conflict
			if (item.status != "succeeded" && item.status != "failed" && item.status != "closed") || !errors.As(sealErr, &unavailable) || unavailable.Code != "payment_credentials_unavailable" {
				return sealErr
			}
			// Lost credentials on historical terminal payments must never resolve
			// against a replacement merchant, but need not prevent new sales.
			sealed, sealErr = s.paymentCredentials(item.reference, []byte(`{"unavailable":true}`), true)
			if sealErr != nil {
				return sealErr
			}
		}
		if _, err = tx.Exec(ctx, "UPDATE payment_transactions SET credential_snapshot=$2 WHERE id=$1", item.id, sealed); err != nil {
			return err
		}
	}
	return nil
}

// Local expiry ends the merchant-rotation hold, not the upstream payment link.
// Expired real intents still block another payment and accept late settlement.
func (s *Store) ExpirePaymentIntents(ctx context.Context) (int64, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err = lockPaymentConfiguration(ctx, tx); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `UPDATE payment_transactions SET status='expired',updated_at=clock_timestamp(),
 next_reconcile_at=CASE WHEN provider=ANY($1::text[]) THEN clock_timestamp() ELSE NULL END,
 auto_reconcile_attempts=CASE WHEN provider=ANY($1::text[]) THEN 0 ELSE auto_reconcile_attempts END
 WHERE status='pending' AND provider<>'mock' AND expires_at<=clock_timestamp() RETURNING id,invoice_id,provider`, payment.QueryMethods())
	if err != nil {
		return 0, err
	}
	type expired struct {
		id, invoice int64
		provider    string
	}
	var list []expired
	for rows.Next() {
		var item expired
		if err = rows.Scan(&item.id, &item.invoice, &item.provider); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, item := range list {
		if err = Audit(ctx, tx, 0, "payment.intent.expired", "invoice", item.invoice, map[string]any{"payment_id": item.id, "provider": item.provider, "upstream_closed": false}); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int64(len(list)), nil
}

func realPaymentProvider(provider string) bool {
	method, registered := payment.MethodInfo(provider)
	return registered && !method.Simulation
}
