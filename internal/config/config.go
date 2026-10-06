package config

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/codex/zjmfgo/internal/provision"
)

type Config struct {
	HTTPAddr, DatabaseURL, PublicOrigin, PaymentGatewaySecret string
	EpayAPIURL, EpayPID, EpayKey                              string
	EpayTypes                                                 []string
	SecureCookie                                              bool
	SMTPHost, SMTPUsername, SMTPPassword, SMTPFrom            string
	SMTPPort                                                  int
	MFAEncryptionKey                                          []byte
	PurchaseFieldEncryptionKey                                []byte
	NOKVMProfiles                                             map[string]*provision.NOKVM
	ProvisionEncryptionKey                                    []byte
}

func Load() (Config, error) {
	smtpPort, err := strconv.Atoi(env("SMTP_PORT", "587"))
	if err != nil || smtpPort < 1 || smtpPort > 65535 {
		return Config{}, fmt.Errorf("SMTP_PORT must be between 1 and 65535")
	}
	c := Config{HTTPAddr: env("HTTP_ADDR", "127.0.0.1:8088"), DatabaseURL: os.Getenv("DATABASE_URL"), PublicOrigin: strings.TrimRight(env("PUBLIC_ORIGIN", "http://localhost:3088"), "/"), PaymentGatewaySecret: os.Getenv("PAYMENT_GATEWAY_SECRET"), EpayAPIURL: strings.TrimRight(strings.TrimSpace(os.Getenv("EPAY_API_URL")), "/"), EpayPID: strings.TrimSpace(os.Getenv("EPAY_PID")), EpayKey: os.Getenv("EPAY_KEY"), SecureCookie: os.Getenv("COOKIE_SECURE") == "true", SMTPHost: strings.TrimSpace(os.Getenv("SMTP_HOST")), SMTPPort: smtpPort, SMTPUsername: os.Getenv("SMTP_USERNAME"), SMTPPassword: os.Getenv("SMTP_PASSWORD"), SMTPFrom: strings.TrimSpace(os.Getenv("SMTP_FROM"))}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	if c.EpayAPIURL != "" || c.EpayPID != "" || c.EpayKey != "" || os.Getenv("EPAY_TYPES") != "" {
		seen := map[string]bool{}
		for _, value := range strings.Split(env("EPAY_TYPES", "alipay"), ",") {
			value = strings.TrimSpace(value)
			if seen[value] || !payment.ValidEpayType(value) {
				return c, fmt.Errorf("EPAY_TYPES is invalid")
			}
			seen[value] = true
			c.EpayTypes = append(c.EpayTypes, value)
		}
		g := payment.EpayGateway{EpayConfig: payment.EpayConfig{APIURL: c.EpayAPIURL, PID: c.EpayPID, Key: c.EpayKey, PublicOrigin: c.PublicOrigin, Types: c.EpayTypes}, Type: c.EpayTypes[0]}
		if g.Validate() != nil {
			return c, fmt.Errorf("EPAY configuration is invalid; use HTTPS or loopback URLs and complete merchant credentials")
		}
	}
	if raw := strings.TrimSpace(os.Getenv("MFA_ENCRYPTION_KEY")); raw != "" {
		key, decodeErr := hex.DecodeString(raw)
		if decodeErr != nil || len(key) != 32 {
			return c, fmt.Errorf("MFA_ENCRYPTION_KEY must be 64 hexadecimal characters")
		}
		c.MFAEncryptionKey = key
	}
	if raw := strings.TrimSpace(os.Getenv("PURCHASE_FIELD_ENCRYPTION_KEY")); raw != "" {
		key, decodeErr := hex.DecodeString(raw)
		if decodeErr != nil || len(key) != 32 {
			return c, fmt.Errorf("PURCHASE_FIELD_ENCRYPTION_KEY must be 64 hexadecimal characters")
		}
		c.PurchaseFieldEncryptionKey = key
	}
	if raw := strings.TrimSpace(os.Getenv("PROVISION_ENCRYPTION_KEY")); raw != "" {
		key, decodeErr := hex.DecodeString(raw)
		if decodeErr != nil || len(key) != 32 {
			return c, fmt.Errorf("PROVISION_ENCRYPTION_KEY must be 64 hexadecimal characters")
		}
		c.ProvisionEncryptionKey = key
	}
	c.NOKVMProfiles = map[string]*provision.NOKVM{}
	if raw := strings.TrimSpace(os.Getenv("NOKVM_PROFILES")); raw != "" {
		var profiles []provision.Profile
		if len(raw) > 65536 || json.Unmarshal([]byte(raw), &profiles) != nil || len(profiles) > 32 || len(c.ProvisionEncryptionKey) != 32 {
			return c, fmt.Errorf("NOKVM_PROFILES requires a profile array and PROVISION_ENCRYPTION_KEY")
		}
		for _, profile := range profiles {
			n, profileErr := provision.New(profile)
			if profileErr != nil || c.NOKVMProfiles[profile.ID] != nil {
				return c, fmt.Errorf("NOKVM_PROFILES contains an invalid or duplicate profile")
			}
			c.NOKVMProfiles[profile.ID] = n
		}
	}
	return c, nil
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (c Config) PaymentQueryGateways() map[string]payment.QueryGateway {
	gateways := make(map[string]payment.QueryGateway, len(c.EpayTypes))
	for _, kind := range c.EpayTypes {
		g := payment.EpayGateway{EpayConfig: payment.EpayConfig{APIURL: c.EpayAPIURL, PID: c.EpayPID, Key: c.EpayKey, PublicOrigin: c.PublicOrigin, Types: c.EpayTypes}, Type: kind}
		if g.Validate() == nil {
			gateways[g.Name()] = g
		}
	}
	return gateways
}
