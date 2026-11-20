package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go_backend_boilerplate/internal/config"
)

// DefaultHCaptchaVerifyURL is the official hCaptcha siteverify endpoint.
const DefaultHCaptchaVerifyURL = "https://hcaptcha.com/siteverify"

var (
	// ErrEmptyCaptchaToken is returned when token verification is attempted with an empty token string.
	ErrEmptyCaptchaToken = errors.New("empty captcha token")
	// ErrEmptyCaptchaSecret is returned when the verifier has no configured secret.
	ErrEmptyCaptchaSecret = errors.New("empty captcha secret")
)

// CaptchaOption allows customizing the CaptchaVerifier configuration.
type CaptchaOption func(*CaptchaVerifier)

// WithCaptchaVerifyURL overrides the endpoint URL used for token verification (useful in tests).
func WithCaptchaVerifyURL(urlStr string) CaptchaOption {
	return func(v *CaptchaVerifier) {
		v.verifyURL = urlStr
	}
}

// WithCaptchaHTTPClient configures a custom HTTP client for the CaptchaVerifier.
func WithCaptchaHTTPClient(client *http.Client) CaptchaOption {
	return func(v *CaptchaVerifier) {
		v.client = client
	}
}

// CaptchaVerifier queries the hCaptcha siteverify endpoint to validate client response tokens.
type CaptchaVerifier struct {
	secret    string
	verifyURL string
	client    *http.Client
}

// NewCaptchaVerifier constructs a CaptchaVerifier with the given secret key.
func NewCaptchaVerifier(secret string, opts ...CaptchaOption) *CaptchaVerifier {
	v := &CaptchaVerifier{
		secret:    secret,
		verifyURL: DefaultHCaptchaVerifyURL,
		client:    &http.Client{Timeout: 5 * time.Second},
	}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// NewCaptchaVerifierFromConfig creates a CaptchaVerifier using the secret in SecurityConfig.
func NewCaptchaVerifierFromConfig(cfg config.SecurityConfig, opts ...CaptchaOption) *CaptchaVerifier {
	return NewCaptchaVerifier(cfg.HCaptchaSecret, opts...)
}

// HCaptchaResponse represents the payload returned by https://hcaptcha.com/siteverify.
type HCaptchaResponse struct {
	Success     bool     `json:"success"`
	ChallengeTS string   `json:"challenge_ts,omitempty"`
	Hostname    string   `json:"hostname,omitempty"`
	Credit      bool     `json:"credit,omitempty"`
	ErrorCodes  []string `json:"error-codes,omitempty"`
}

// VerifyToken sends a siteverify request to validate the client-submitted captcha token.
func (v *CaptchaVerifier) VerifyToken(ctx context.Context, token, clientIP string) (bool, error) {
	if strings.TrimSpace(token) == "" {
		return false, ErrEmptyCaptchaToken
	}
	if strings.TrimSpace(v.secret) == "" {
		return false, ErrEmptyCaptchaSecret
	}

	data := url.Values{}
	data.Set("secret", v.secret)
	data.Set("response", token)
	if strings.TrimSpace(clientIP) != "" {
		data.Set("remoteip", clientIP)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.verifyURL, strings.NewReader(data.Encode()))
	if err != nil {
		return false, fmt.Errorf("failed to create captcha request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := v.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("captcha verification request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("unexpected captcha response status: %d", resp.StatusCode)
	}

	var res HCaptchaResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return false, fmt.Errorf("failed to decode captcha response: %w", err)
	}

	return res.Success, nil
}
