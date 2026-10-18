package store

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/payment"
	"github.com/jackc/pgx/v5"
)

// PaymentRuntime is a private-to-the-server, immutable instance read. Factories
// construct local checkout links only; external order creation needs a separate
// durable reservation protocol before it can use the financial core.
type PaymentRuntime struct {
	InstanceID       string
	Implementation   string
	Version          int64
	SchemaVersion    int
	Config           module.Config
	Legacy           bool
	SingletonVersion int64
}

func (c PaymentConfiguration) ImplementationID() string {
	if c.Runtime != nil {
		return c.Runtime.Implementation
	}
	return "epay"
}

// PaymentConfigurationForInstanceTx uses the caller's connection. Legacy rows
// remain backed by payment_settings, whose lock must precede any channel lock.
func (s *Store) PaymentConfigurationForInstanceTx(ctx context.Context, tx pgx.Tx, id string, fallback PaymentConfiguration) (PaymentConfiguration, error) {
	id = strings.TrimSpace(id)
	legacy := id == "payment-default" || id == "payment-mock-default"
	query := "SELECT " + channelInstanceColumns + " FROM channel_instances WHERE id=$1"
	if tx != nil && !legacy {
		query += " FOR SHARE"
	}
	var row pgx.Row
	if tx == nil {
		row = s.Pool.QueryRow(ctx, query, id)
	} else {
		row = tx.QueryRow(ctx, query, id)
	}
	rec, err := scanChannelRecord(row)
	if err != nil {
		return PaymentConfiguration{}, err
	}
	i := rec.item
	if i.Category != string(channel.Payment) || (!i.Enabled && id != "payment-mock-default") {
		return PaymentConfiguration{}, conflict("payment_provider_disabled", "支付渠道已关闭")
	}
	if err = validatePaymentModuleContract(i.ImplementationID, i.SchemaVersion); err != nil {
		return PaymentConfiguration{}, err
	}
	runtime := &PaymentRuntime{InstanceID: id, Implementation: i.ImplementationID, Version: i.Version, SchemaVersion: i.SchemaVersion, Legacy: legacy}
	if legacy {
		expectedImplementation := "epay"
		if id == "payment-mock-default" {
			expectedImplementation = "mock"
		}
		if i.ImplementationID != expectedImplementation {
			return PaymentConfiguration{}, conflict("payment_provider_disabled", "默认支付实例与实现不匹配")
		}
		out, err := s.PaymentConfigurationTx(ctx, tx, fallback)
		if err != nil {
			return PaymentConfiguration{}, err
		}
		runtime.SingletonVersion = out.Version
		out.Runtime, out.Version = runtime, i.Version
		if id == "payment-mock-default" {
			if !out.MockEnabled {
				return PaymentConfiguration{}, conflict("payment_provider_disabled", "支付渠道已关闭")
			}
			out.Enabled = true
		}
		return out, nil
	}
	cfg, implementation, err := s.runtimeChannelRecord(rec, channel.Payment)
	if err != nil {
		return PaymentConfiguration{}, err
	}
	cfg.PublicOrigin = fallback.PublicOrigin
	// The one existing simulator deliberately uses the deployment credential.
	if implementation == "mock" && cfg.Secret == "" {
		cfg.Secret = fallback.MockSecret
	}
	runtime.Config = cfg
	return PaymentConfiguration{PaymentSettings: PaymentSettings{Configured: true, Enabled: true, MockEnabled: implementation == "mock", Version: i.Version, EncryptionConfigured: len(s.ProvisionEncryptionKey) == 32}, MockSecret: fallback.MockSecret, PublicOrigin: fallback.PublicOrigin, Runtime: runtime}, nil
}

type paymentRuntimeSnapshot struct {
	SnapshotVersion int               `json:"snapshot_version"`
	Implementation  string            `json:"implementation"`
	Method          string            `json:"method"`
	SchemaVersion   int               `json:"schema_version"`
	InstanceID      string            `json:"instance_id"`
	InstanceVersion int64             `json:"instance_version"`
	MerchantScope   string            `json:"merchant_scope"`
	Values          map[string]any    `json:"values"`
	Secret          string            `json:"secret,omitempty"`
	Secrets         map[string]string `json:"secrets,omitempty"`
	PublicOrigin    string            `json:"public_origin"`
}

var paymentMerchantScope = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}:[0-9a-f]{64}$`)

func validPaymentMerchantScope(method, scope string) bool {
	if method == "mock" {
		return scope == "mock:test-only"
	}
	return paymentMerchantScope.MatchString(scope)
}

func (s *Store) snapshotRuntimePayment(reference, method string, runtime *PaymentRuntime) ([]byte, error) {
	if runtime == nil || runtime.Legacy || runtime.InstanceID == "" || runtime.Version < 1 || runtime.SchemaVersion < 1 || runtime.Config.Payload != nil {
		return nil, conflict("payment_credentials_unavailable", "支付渠道快照不可用")
	}
	gateway, err := payment.NewFor(runtime.Implementation, method, runtime.Config)
	if err != nil {
		return nil, conflict("payment_credentials_unavailable", "支付渠道凭据不可用")
	}
	scope := ""
	if scoped, ok := gateway.(payment.MerchantScopeGateway); ok {
		scope, err = scoped.MerchantScope()
		if err != nil || !validPaymentMerchantScope(method, scope) {
			return nil, conflict("payment_credentials_unavailable", "支付商户身份不可核验")
		}
	} else {
		return nil, conflict("payment_credentials_unavailable", "支付商户身份不可核验")
	}
	snapshot := paymentRuntimeSnapshot{SnapshotVersion: 2, Implementation: runtime.Implementation, Method: method, SchemaVersion: runtime.SchemaVersion, InstanceID: runtime.InstanceID, InstanceVersion: runtime.Version, MerchantScope: scope, Values: runtime.Config.Values, Secret: runtime.Config.Secret, Secrets: runtime.Config.Secrets, PublicOrigin: runtime.Config.PublicOrigin}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return s.paymentCredentials(reference, raw, true)
}

func runtimePaymentGateway(raw []byte, provider, instanceID string) (payment.Gateway, bool, error) {
	var saved paymentRuntimeSnapshot
	if err := json.Unmarshal(raw, &saved); err != nil {
		return nil, false, errors.New("payment credentials unavailable")
	}
	owner, registered := payment.MethodOwner(provider)
	if saved.SnapshotVersion != 2 || !registered || owner != saved.Implementation || saved.Method != provider || saved.InstanceID == "" || saved.InstanceID != instanceID || saved.InstanceVersion < 1 || saved.SchemaVersion < 1 || !validPaymentMerchantScope(provider, saved.MerchantScope) {
		return nil, false, errors.New("payment credentials unavailable")
	}
	if err := validatePaymentModuleContract(saved.Implementation, saved.SchemaVersion); err != nil {
		return nil, false, err
	}
	gateway, err := payment.NewFor(saved.Implementation, provider, module.Config{Values: saved.Values, Secret: saved.Secret, Secrets: saved.Secrets, PublicOrigin: saved.PublicOrigin})
	if err != nil {
		return nil, false, errors.New("payment credentials unavailable")
	}
	if scoped, ok := gateway.(payment.MerchantScopeGateway); ok {
		scope, err := scoped.MerchantScope()
		if err != nil || scope != saved.MerchantScope {
			return nil, false, errors.New("payment credentials unavailable")
		}
	} else if saved.MerchantScope != "" {
		return nil, false, errors.New("payment credentials unavailable")
	}
	return gateway, true, nil
}

func registeredPaymentQuery(provider string) bool {
	for _, method := range payment.QueryMethods() {
		if method == provider {
			return true
		}
	}
	return false
}
