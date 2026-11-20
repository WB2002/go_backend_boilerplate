package logger

import (
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

var (
	emailRegex        = regexp.MustCompile(`(?i)[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
	bearerRegex       = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9\-._~+/]+=*`)
	passwordJSONRegex = regexp.MustCompile(`(?i)("(?i:password|pass|secret|token|apiKey|api_key|access_token|refresh_token|authorization)"\s*:\s*")[^"]*(")`)
	passwordRawRegex  = regexp.MustCompile(`(?i)\b(password|pass|secret|token|apiKey|api_key|access_token|refresh_token|authorization)=([^"'\s&]+)`)

	defaultLoggerOnce sync.Once
	defaultLogger     *zerolog.Logger
)

// MaskingWriter wraps an io.Writer and applies regex-based PII redaction to all logged bytes.
type MaskingWriter struct {
	writer io.Writer
}

// NewMaskingWriter creates a new MaskingWriter wrapping w.
func NewMaskingWriter(w io.Writer) *MaskingWriter {
	return &MaskingWriter{writer: w}
}

// Write applies PII masking before passing bytes to the underlying writer.
func (mw *MaskingWriter) Write(p []byte) (n int, err error) {
	masked := MaskBytes(p)
	_, err = mw.writer.Write(masked)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// MaskPII replaces sensitive patterns (emails, bearer tokens, passwords) with redacted placeholders.
func MaskPII(s string) string {
	res := bearerRegex.ReplaceAllString(s, "Bearer [REDACTED_TOKEN]")
	res = emailRegex.ReplaceAllString(res, "[REDACTED_EMAIL]")
	res = passwordJSONRegex.ReplaceAllString(res, "${1}[REDACTED]${2}")
	res = passwordRawRegex.ReplaceAllString(res, "${1}=[REDACTED]")
	return res
}

// MaskBytes applies MaskPII to a byte slice.
func MaskBytes(b []byte) []byte {
	return []byte(MaskPII(string(b)))
}

// New creates a production-ready Zerolog logger configured for the given environment.
func New(env string) *zerolog.Logger {
	return NewWithWriter(os.Stdout, env)
}

// NewWithWriter constructs a Zerolog logger sending masked output to w.
func NewWithWriter(w io.Writer, env string) *zerolog.Logger {
	zerolog.TimeFieldFormat = time.RFC3339Nano

	maskingWriter := NewMaskingWriter(w)

	var output io.Writer = maskingWriter
	if strings.ToLower(env) == "development" {
		output = zerolog.ConsoleWriter{
			Out:        maskingWriter,
			TimeFormat: "15:04:05.000",
		}
	}

	l := zerolog.New(output).With().Timestamp().Logger()
	return &l
}

// Default returns a shared singleton logger.
func Default() *zerolog.Logger {
	defaultLoggerOnce.Do(func() {
		defaultLogger = New("production")
	})
	return defaultLogger
}
