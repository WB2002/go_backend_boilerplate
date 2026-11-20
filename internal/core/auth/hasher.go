package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

var (
	// ErrBulkheadQueueFull indicates the hashing bulkhead waiting queue is at maximum capacity.
	ErrBulkheadQueueFull = errors.New("argon2id bulkhead queue capacity exceeded")
	// ErrBulkheadTimeout indicates waiting in the bulkhead queue timed out before acquiring a slot.
	ErrBulkheadTimeout = errors.New("argon2id bulkhead wait timeout exceeded")
	// ErrInvalidHash indicates the argon2id hash string is corrupt or in an unsupported format.
	ErrInvalidHash = errors.New("invalid argon2id hash format")
)

// PasswordHasher defines the contract for secure password hashing and verification.
type PasswordHasher interface {
	HashPassword(password string) (string, error)
	VerifyPassword(password, hash string) (bool, error)
	HashPasswordContext(ctx context.Context, password string) (string, error)
	VerifyPasswordContext(ctx context.Context, password, hash string) (bool, error)
}

// Argon2Params encapsulates configuration parameters for Argon2id key derivation.
type Argon2Params struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultArgon2Params returns OWASP-recommended default parameters for Argon2id.
func DefaultArgon2Params() Argon2Params {
	return Argon2Params{
		Memory:      65536, // 64 MB
		Iterations:  3,
		Parallelism: 2,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// Argon2idHasher protects the Go runtime scheduler and CPU against thread starvation
// by bounding concurrent Argon2id hashing operations using a semaphore and wait queue bulkhead.
type Argon2idHasher struct {
	params        Argon2Params
	maxConcurrent int
	queueDepth    int
	maxWait       time.Duration
	sem           chan struct{}
	mu            sync.Mutex
	waiting       int
}

// NewArgon2idHasher creates a new Argon2idHasher with default Argon2id parameters.
func NewArgon2idHasher(maxConcurrent, queueDepth int, maxWait time.Duration) *Argon2idHasher {
	return NewArgon2idHasherWithParams(DefaultArgon2Params(), maxConcurrent, queueDepth, maxWait)
}

// NewArgon2idHasherWithParams creates a new Argon2idHasher with custom parameters.
func NewArgon2idHasherWithParams(params Argon2Params, maxConcurrent, queueDepth int, maxWait time.Duration) *Argon2idHasher {
	if maxConcurrent <= 0 {
		maxConcurrent = 2
	}
	if queueDepth <= 0 {
		queueDepth = 4
	}
	if maxWait <= 0 {
		maxWait = 2 * time.Second
	}
	return &Argon2idHasher{
		params:        params,
		maxConcurrent: maxConcurrent,
		queueDepth:    queueDepth,
		maxWait:       maxWait,
		sem:           make(chan struct{}, maxConcurrent),
	}
}

// acquireSlot acquires an execution slot in the bulkhead. If slots are busy,
// it queues up to queueDepth for at most maxWait duration.
func (h *Argon2idHasher) acquireSlot(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Fast path: acquire immediately if slot available
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case h.sem <- struct{}{}:
		return func() { <-h.sem }, nil
	default:
	}

	// Queue path: check queue depth
	h.mu.Lock()
	if h.waiting >= h.queueDepth {
		h.mu.Unlock()
		return nil, ErrBulkheadQueueFull
	}
	h.waiting++
	h.mu.Unlock()

	timer := time.NewTimer(h.maxWait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		h.mu.Lock()
		h.waiting--
		h.mu.Unlock()
		return nil, ctx.Err()
	case h.sem <- struct{}{}:
		h.mu.Lock()
		h.waiting--
		h.mu.Unlock()
		return func() { <-h.sem }, nil
	case <-timer.C:
		h.mu.Lock()
		h.waiting--
		h.mu.Unlock()
		return nil, ErrBulkheadTimeout
	}
}

// HashPasswordContext hashes a plain-text password using Argon2id bounded by the bulkhead with context support.
func (h *Argon2idHasher) HashPasswordContext(ctx context.Context, password string) (string, error) {
	release, err := h.acquireSlot(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	salt := make([]byte, h.params.SaltLength)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("failed to generate random salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, h.params.Iterations, h.params.Memory, h.params.Parallelism, h.params.KeyLength)

	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Key := base64.RawStdEncoding.EncodeToString(key)

	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.params.Memory, h.params.Iterations, h.params.Parallelism, b64Salt, b64Key)

	return encoded, nil
}

// HashPassword hashes a plain-text password using Argon2id bounded by the bulkhead.
func (h *Argon2idHasher) HashPassword(password string) (string, error) {
	return h.HashPasswordContext(context.Background(), password)
}

// VerifyPasswordContext checks a plain-text password against an Argon2id hash string with context support.
func (h *Argon2idHasher) VerifyPasswordContext(ctx context.Context, password, hash string) (bool, error) {
	params, salt, key, err := parseArgon2idHash(hash)
	if err != nil {
		return false, err
	}

	release, err := h.acquireSlot(ctx)
	if err != nil {
		return false, err
	}
	defer release()

	testKey := argon2.IDKey([]byte(password), salt, params.Iterations, params.Memory, params.Parallelism, uint32(len(key)))

	if subtle.ConstantTimeCompare(key, testKey) == 1 {
		return true, nil
	}
	return false, nil
}

// VerifyPassword checks a plain-text password against an Argon2id hash string.
func (h *Argon2idHasher) VerifyPassword(password, hash string) (bool, error) {
	return h.VerifyPasswordContext(context.Background(), password, hash)
}

func parseArgon2idHash(encoded string) (*Argon2Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return nil, nil, nil, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return nil, nil, nil, ErrInvalidHash
	}

	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return nil, nil, nil, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, nil, nil, ErrInvalidHash
	}

	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return nil, nil, nil, ErrInvalidHash
	}

	return &Argon2Params{
		Memory:      memory,
		Iterations:  iterations,
		Parallelism: parallelism,
		SaltLength:  uint32(len(salt)),
		KeyLength:   uint32(len(key)),
	}, salt, key, nil
}
