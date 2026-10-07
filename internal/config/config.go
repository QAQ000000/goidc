package config

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/codex/zjmfgo/internal/provision"
)

type Config struct {
	HTTPAddr, DatabaseURL, PublicOrigin, PaymentGatewaySecret                 string
	PublicOrigins                                                             []string
	EpayAPIURL, EpayPID, EpayKey                                              string
	EpayTypes                                                                 []string
	PaymentMockEnabled                                                        bool
	SecureCookie                                                              bool
	SMTPHost, SMTPUsername, SMTPPassword, SMTPFrom, SMTPFromName, SMTPTLSMode string
	SMTPPort                                                                  int
	MFAEncryptionKey                                                          []byte
	PurchaseFieldEncryptionKey                                                []byte
	NOKVMProfiles                                                             map[string]*provision.NOKVM
	ProvisionEncryptionKey                                                    []byte
}

func Load() (Config, error) {
	mockEnabled, err := strconv.ParseBool(env("PAYMENT_MOCK_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("PAYMENT_MOCK_ENABLED must be a boolean")
	}
	smtpPort, err := strconv.Atoi(env("SMTP_PORT", "587"))
	if err != nil || smtpPort < 1 || smtpPort > 65535 {
		return Config{}, fmt.Errorf("SMTP_PORT must be between 1 and 65535")
	}
	publicOrigin := strings.TrimRight(strings.TrimSpace(env("PUBLIC_ORIGIN", "http://localhost:3088")), "/")
	publicOrigins, err := parsePublicOrigins(publicOrigin, os.Getenv("PUBLIC_ORIGINS"))
	if err != nil {
		return Config{}, err
	}
	c := Config{HTTPAddr: env("HTTP_ADDR", "127.0.0.1:8088"), DatabaseURL: os.Getenv("DATABASE_URL"), PublicOrigin: publicOrigin, PublicOrigins: publicOrigins, PaymentGatewaySecret: os.Getenv("PAYMENT_GATEWAY_SECRET"), EpayAPIURL: strings.TrimRight(strings.TrimSpace(os.Getenv("EPAY_API_URL")), "/"), EpayPID: strings.TrimSpace(os.Getenv("EPAY_PID")), EpayKey: os.Getenv("EPAY_KEY"), SecureCookie: os.Getenv("COOKIE_SECURE") == "true", SMTPHost: strings.TrimSpace(os.Getenv("SMTP_HOST")), SMTPPort: smtpPort, SMTPUsername: os.Getenv("SMTP_USERNAME"), SMTPPassword: os.Getenv("SMTP_PASSWORD"), SMTPFrom: strings.TrimSpace(os.Getenv("SMTP_FROM"))}
	c.PaymentMockEnabled = mockEnabled
	c.SMTPFromName = strings.TrimSpace(os.Getenv("SMTP_FROM_NAME"))
	c.SMTPTLSMode = strings.TrimSpace(os.Getenv("SMTP_TLS_MODE"))
	if c.SMTPTLSMode == "" {
		if c.SMTPPort == 465 {
			c.SMTPTLSMode = "implicit_tls"
		} else {
			c.SMTPTLSMode = "starttls"
		}
	}
	if c.SMTPTLSMode != "implicit_tls" && c.SMTPTLSMode != "starttls" && c.SMTPTLSMode != "none" || c.SMTPTLSMode == "none" && (c.SMTPUsername != "" || c.SMTPPassword != "") || strings.ContainsAny(c.SMTPFromName, "\r\n\x00") {
		return c, fmt.Errorf("SMTP configuration is invalid")
	}
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

func parsePublicOrigins(primary, raw string) ([]string, error) {
	values := []string{primary}
	if strings.TrimSpace(raw) != "" {
		values = append(values, strings.Split(raw, ",")...)
	}
	seen := make(map[string]struct{}, len(values))
	origins := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimRight(strings.TrimSpace(value), "/")
		parsed, err := url.Parse(value)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("PUBLIC_ORIGINS contains an invalid origin")
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		origins = append(origins, value)
	}
	return origins, nil
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
