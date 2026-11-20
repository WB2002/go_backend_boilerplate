package email

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"
)

// SMTPConfig holds connection parameters for an SMTP gateway.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

// SMTPMailer dispatches emails through standard SMTP protocols.
type SMTPMailer struct {
	cfg SMTPConfig
}

// NewSMTPMailer constructs a new SMTPMailer.
func NewSMTPMailer(cfg SMTPConfig) *SMTPMailer {
	return &SMTPMailer{cfg: cfg}
}

// Send delivers the message via net/smtp.
func (s *SMTPMailer) Send(ctx context.Context, msg Message) error {
	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)

	var auth smtp.Auth
	if s.cfg.Username != "" && s.cfg.Password != "" {
		auth = smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
	}

	from := s.cfg.From
	if from == "" {
		from = "no-reply@example.com"
	}

	headers := make(map[string]string)
	headers["From"] = from
	headers["To"] = msg.To
	headers["Subject"] = msg.Subject
	headers["MIME-Version"] = "1.0"
	headers["Content-Type"] = "text/html; charset=UTF-8"

	var messageBuilder strings.Builder
	for k, v := range headers {
		messageBuilder.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}
	messageBuilder.WriteString("\r\n")

	body := msg.BodyHTML
	if body == "" {
		body = msg.BodyText
	}
	messageBuilder.WriteString(body)

	return smtp.SendMail(addr, auth, from, []string{msg.To}, []byte(messageBuilder.String()))
}
