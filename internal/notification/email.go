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
	"github.com/codex/zjmfgo/internal/store"
)

func init() {
	channel.MustRegister(channel.Descriptor{
		ID: "smtp", Name: "SMTP", Category: channel.Mail,
		Capabilities: []string{"send", "test"},
		Fields: []channel.Field{
			{Key: "host", Label: "SMTP 主机", Type: channel.Text, Required: true},
			{Key: "port", Label: "SMTP 端口", Type: channel.Number, Required: true},
			{Key: "username", Label: "用户名", Type: channel.Text},
			{Key: "password", Label: "密码", Type: channel.Text, Secret: true},
			{Key: "from", Label: "发件地址", Type: channel.Text, Required: true},
			{Key: "from_name", Label: "发件名称", Type: channel.Text},
			{Key: "tls_mode", Label: "TLS 模式", Type: channel.Select, Options: []string{"implicit_tls", "starttls", "none"}},
		},
	})
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
