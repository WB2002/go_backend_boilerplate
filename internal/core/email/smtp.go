package email

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

const defaultSMTPDialTimeout = 30 * time.Second

var headerNewlineReplacer = strings.NewReplacer("\r", "", "\n", "")

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

// Send delivers the message via net/smtp while honoring context deadlines and cancellation.
func (s *SMTPMailer) Send(ctx context.Context, msg Message) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("smtp send aborted: %w", err)
	}

	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprintf("%d", s.cfg.Port))

	var auth smtp.Auth
	if s.cfg.Username != "" && s.cfg.Password != "" {
		auth = smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
	}

	from := sanitizeHeaderValue(s.cfg.From)
	if from == "" {
		from = "no-reply@example.com"
	}
	to := sanitizeHeaderValue(msg.To)
	subject := sanitizeHeaderValue(msg.Subject)

	rawMessage := buildRawMessage(from, to, subject, msg)

	dialer := &net.Dialer{Timeout: defaultSMTPDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return wrapContextErr(ctx, "smtp dial", err)
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
			_ = conn.Close()
		case <-done:
		}
	}()

	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return wrapContextErr(ctx, "smtp handshake", err)
	}
	defer func() {
		_ = client.Close()
	}()

	if ok, _ := client.Extension("STARTTLS"); ok {
		tlsCfg := &tls.Config{
			ServerName: s.cfg.Host,
			MinVersion: tls.VersionTLS12,
		}
		if err := client.StartTLS(tlsCfg); err != nil {
			return wrapContextErr(ctx, "smtp starttls", err)
		}
	}

	if auth != nil {
		if ok, _ := client.Extension("AUTH"); !ok {
			return errors.New("smtp: server does not support AUTH")
		}
		if err := client.Auth(auth); err != nil {
			return wrapContextErr(ctx, "smtp auth", err)
		}
	}

	if err := client.Mail(from); err != nil {
		return wrapContextErr(ctx, "smtp mail from", err)
	}
	if err := client.Rcpt(to); err != nil {
		return wrapContextErr(ctx, "smtp rcpt to", err)
	}

	w, err := client.Data()
	if err != nil {
		return wrapContextErr(ctx, "smtp data", err)
	}
	if _, err := w.Write(rawMessage); err != nil {
		_ = w.Close()
		return wrapContextErr(ctx, "smtp write body", err)
	}
	if err := w.Close(); err != nil {
		return wrapContextErr(ctx, "smtp close data", err)
	}

	if err := client.Quit(); err != nil {
		return wrapContextErr(ctx, "smtp quit", err)
	}

	return nil
}

func sanitizeHeaderValue(value string) string {
	return headerNewlineReplacer.Replace(value)
}

func buildRawMessage(from, to, subject string, msg Message) []byte {
	contentType := "text/plain; charset=UTF-8"
	body := msg.BodyText
	if msg.BodyHTML != "" {
		contentType = "text/html; charset=UTF-8"
		body = msg.BodyHTML
	}

	headers := []struct{ k, v string }{
		{"From", from},
		{"To", to},
		{"Subject", subject},
		{"MIME-Version", "1.0"},
		{"Content-Type", contentType},
	}

	var b strings.Builder
	for _, h := range headers {
		b.WriteString(h.k)
		b.WriteString(": ")
		b.WriteString(h.v)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

func wrapContextErr(ctx context.Context, op string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s: %w: %v", op, ctxErr, err)
	}
	return fmt.Errorf("%s: %w", op, err)
}
