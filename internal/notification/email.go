package notification

import (
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/store"
)

type MailProvider interface {
	Enabled() bool
	Send(store.EmailJob) error
}

var mailRegistry = module.NewRegistry[MailProvider](channel.Mail)

// RegisterMailProvider is the typed composition boundary for compiled mail
// adapters. Runtime code dispatches using the persisted implementation ID.
func RegisterMailProvider(desc channel.Descriptor, factory module.Factory[MailProvider]) error {
	return mailRegistry.Register(desc, factory)
}

func NewMailProvider(id string, config module.Config) (MailProvider, error) {
	return mailRegistry.New(id, config)
}

// SendMail uses the same dispatch path for SMTP and other compiled adapters.
// Provider errors can contain credentials or message contents, so the worker
// receives a fixed error suitable for logs and outbox failure records.
func SendMail(id string, config module.Config, job store.EmailJob) error {
	provider, err := NewMailProvider(id, config)
	if err != nil || provider == nil || !provider.Enabled() {
		return fmt.Errorf("mail channel unavailable")
	}
	if err = provider.Send(job); err != nil {
		return fmt.Errorf("mail delivery failed")
	}
	return nil
}

func init() {
	minPort, maxPort := 1.0, 65535.0
	mailRegistry.MustRegister(channel.Descriptor{
		ID: "smtp", Name: "SMTP", Category: channel.Mail,
		Capabilities: []string{"send", "test"},
		Fields: []channel.Field{
			{Key: "host", Label: "SMTP 主机", Type: channel.Text, Required: true},
			{Key: "port", Label: "SMTP 端口", Type: channel.Number, Required: true, Integer: true, Min: &minPort, Max: &maxPort},
			{Key: "username", Label: "用户名", Type: channel.Text},
			{Key: "password", Label: "密码", Type: channel.Text, Secret: true},
			{Key: "from", Label: "发件地址", Type: channel.Text, Required: true},
			{Key: "from_name", Label: "发件名称", Type: channel.Text},
			{Key: "tls_mode", Label: "TLS 模式", Type: channel.Select, Options: []string{"implicit_tls", "starttls", "none"}},
		},
	}, func(config module.Config) (MailProvider, error) {
		return smtpFromConfig(config)
	})
	channel.MustRegisterConfigValidator(channel.Mail, "smtp", func(values map[string]any, secureFields map[string]bool) []channel.ValidationError {
		config := module.Config{Values: values}
		if secureFields["password"] {
			config.Secrets = map[string]string{"password": "configured"}
		}
		if _, err := smtpFromConfig(config); err != nil {
			return []channel.ValidationError{{Field: "", Code: "invalid_smtp"}}
		}
		return nil
	})
}

func smtpFromConfig(config module.Config) (SMTPConfig, error) {
	values := config.Values
	port := 0
	if raw, ok := values["port"].(float64); ok {
		port = int(raw)
		if float64(port) != raw {
			return SMTPConfig{}, fmt.Errorf("invalid SMTP configuration")
		}
	}
	if raw, ok := values["port"].(int); ok {
		port = raw
	}
	password := config.Secrets["password"]
	if password == "" {
		password = config.Secret
	}
	out := SMTPConfig{
		Host: configString(values, "host"), Port: port,
		Username: configString(values, "username"), Password: password,
		From: configString(values, "from"), FromName: configString(values, "from_name"),
		TLSMode: configString(values, "tls_mode"),
	}
	// An unused stored credential does not turn anonymous SMTP into login.
	if out.Username == "" {
		out.Password = ""
	}
	if out.Port == 0 {
		if _, supplied := values["port"]; supplied {
			return SMTPConfig{}, fmt.Errorf("invalid SMTP configuration")
		}
		if out.TLSMode == "implicit_tls" {
			out.Port = 465
		} else {
			out.Port = 587
		}
	}
	if err := out.Validate(); err != nil {
		return SMTPConfig{}, err
	}
	return out, nil
}

func configString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	FromName string
	TLSMode  string
}

func (c SMTPConfig) Enabled() bool {
	return strings.TrimSpace(c.Host) != "" && strings.TrimSpace(c.From) != ""
}

// Validate checks local transport configuration only; it never connects to a
// remote SMTP server. Send still validates headers belonging to each job.
func (c SMTPConfig) Validate() error {
	from, err := mail.ParseAddress(c.From)
	if !c.Enabled() || c.Port < 1 || c.Port > 65535 || err != nil || from.Address != c.From ||
		strings.ContainsAny(c.Host+c.Username+c.From+c.FromName, "\r\n\x00") ||
		strings.ContainsAny(c.Host, " /\\:") || (c.Username == "") != (c.Password == "") {
		return fmt.Errorf("invalid SMTP configuration")
	}
	if c.TLSMode != "" && c.TLSMode != "implicit_tls" && c.TLSMode != "starttls" && c.TLSMode != "none" ||
		c.TLSMode == "none" && (c.Username != "" || c.Password != "") {
		return fmt.Errorf("invalid SMTP configuration")
	}
	return nil
}

func (c SMTPConfig) Send(job store.EmailJob) (err error) {
	// SMTP replies may echo credentials or message contents. Persist only a fixed error.
	defer func() {
		if err != nil {
			err = fmt.Errorf("SMTP delivery failed")
		}
	}()
	if !c.Enabled() {
		return fmt.Errorf("SMTP is not configured")
	}
	from := strings.TrimSpace(c.From)
	if strings.ContainsAny(from+c.FromName+job.Recipient+job.Subject, "\r\n\x00") {
		return fmt.Errorf("invalid mail headers")
	}
	if a, e := mail.ParseAddress(from); e != nil || a.Address != from {
		return fmt.Errorf("invalid sender")
	}
	if a, e := mail.ParseAddress(job.Recipient); e != nil || a.Address != job.Recipient {
		return fmt.Errorf("invalid recipient")
	}
	mode := c.TLSMode
	if mode == "" {
		if c.Port == 465 {
			mode = "implicit_tls"
		} else {
			mode = "starttls"
		}
	}
	if mode != "implicit_tls" && mode != "starttls" && mode != "none" || mode == "none" && (c.Username != "" || c.Password != "") {
		return fmt.Errorf("invalid TLS mode")
	}
	address := net.JoinHostPort(c.Host, fmt.Sprint(c.Port))
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).Dial("tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return err
	}
	tlsConfig := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
	if mode == "implicit_tls" {
		secure := tls.Client(conn, tlsConfig)
		if err = secure.Handshake(); err != nil {
			return err
		}
		conn = secure
	}
	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	if mode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("STARTTLS required")
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	auth := smtp.Auth(nil)
	if c.Username != "" {
		auth = smtp.PlainAuth("", c.Username, c.Password, c.Host)
	}
	if auth != nil {
		if err = client.Auth(auth); err != nil {
			return err
		}
	}
	message := []byte("From: " + (&mail.Address{Name: c.FromName, Address: from}).String() + "\r\n" +
		"To: " + job.Recipient + "\r\n" +
		"Subject: " + mime.QEncoding.Encode("UTF-8", job.Subject) + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" + job.Body + "\r\n")
	if err = client.Mail(from); err != nil {
		return err
	}
	if err = client.Rcpt(job.Recipient); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err = w.Write(message); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	return client.Quit()
}
