package email

import (
	"context"
	"sync"
)

// MockMailer records sent messages in memory for unit and integration testing.
type MockMailer struct {
	mu           sync.Mutex
	SentMessages []Message
}

// NewMockMailer creates a thread-safe MockMailer instance.
func NewMockMailer() *MockMailer {
	return &MockMailer{
		SentMessages: make([]Message, 0),
	}
}

// Send records the email message in memory.
func (m *MockMailer) Send(ctx context.Context, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SentMessages = append(m.SentMessages, msg)
	return nil
}

// Reset clears the sent message history.
func (m *MockMailer) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SentMessages = m.SentMessages[:0]
}
