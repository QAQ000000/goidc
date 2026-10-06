package notification

import (
	"fmt"
	"net/smtp"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
)

type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

func (c SMTPConfig) Enabled() bool {
	return strings.TrimSpace(c.Host) != "" && strings.TrimSpace(c.From) != ""
}

func (c SMTPConfig) Send(job store.EmailJob) error {
	if !c.Enabled() {
		return fmt.Errorf("SMTP is not configured")
	}
	from := strings.TrimSpace(c.From)
	address := fmt.Sprintf("%s:%d", c.Host, c.Port)
	auth := smtp.Auth(nil)
	if c.Username != "" {
		auth = smtp.PlainAuth("", c.Username, c.Password, c.Host)
	}
	message := []byte("From: " + from + "\r\n" +
		"To: " + job.Recipient + "\r\n" +
		"Subject: " + job.Subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" + job.Body + "\r\n")
	return smtp.SendMail(address, auth, from, []string{job.Recipient}, message)
}
