package email

import (
	"context"
)

// Message encapsulates the payload and recipient for an email dispatch.
type Message struct {
	To           string
	Subject      string
	TemplateName string
	BodyHTML     string
	BodyText     string
	Data         map[string]any
}

// Mailer defines the contract for sending transactional emails.
type Mailer interface {
	Send(ctx context.Context, msg Message) error
}
