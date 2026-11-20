package security

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"go_backend_boilerplate/internal/config"
)

var (
	// ErrVirusDetected is returned when the virus scanner detects malicious content.
	ErrVirusDetected = errors.New("virus detected")
	// ErrClamAVUnavailable is returned when connection to ClamAV daemon fails.
	ErrClamAVUnavailable = errors.New("clamav service unavailable")
)

// ClamAVScanner provides TCP-based streaming virus inspection using the ClamAV INSTREAM protocol.
type ClamAVScanner struct {
	addr    string
	timeout time.Duration
}

// NewClamAVScanner constructs a ClamAVScanner for the given host:port and socket timeout.
func NewClamAVScanner(addr string, timeout time.Duration) *ClamAVScanner {
	if strings.TrimSpace(addr) == "" {
		addr = "localhost:3310"
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &ClamAVScanner{
		addr:    addr,
		timeout: timeout,
	}
}

// NewClamAVScannerFromConfig constructs a ClamAVScanner from SecurityConfig settings.
func NewClamAVScannerFromConfig(cfg config.SecurityConfig, timeout time.Duration) *ClamAVScanner {
	return NewClamAVScanner(cfg.ClamAVAddr, timeout)
}

// ScanStream streams bytes from reader to ClamAV daemon via INSTREAM protocol and returns verdict.
func (s *ClamAVScanner) ScanStream(ctx context.Context, reader io.Reader) (bool, string, error) {
	d := net.Dialer{Timeout: s.timeout}
	conn, err := d.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return false, "", fmt.Errorf("%w: %v", ErrClamAVUnavailable, err)
	}
	defer func() { _ = conn.Close() }()

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(s.timeout))
	}

	// Send zINSTREAM command with null terminator
	if _, err := conn.Write([]byte("zINSTREAM\x00")); err != nil {
		return false, "", fmt.Errorf("failed to write instream command: %w", err)
	}

	buf := make([]byte, 32*1024)
	chunkLenBuf := make([]byte, 4)

	for {
		select {
		case <-ctx.Done():
			return false, "", ctx.Err()
		default:
		}

		n, readErr := reader.Read(buf)
		if n > 0 {
			binary.BigEndian.PutUint32(chunkLenBuf, uint32(n))
			if _, err := conn.Write(chunkLenBuf); err != nil {
				return false, "", fmt.Errorf("failed to write chunk size: %w", err)
			}
			if _, err := conn.Write(buf[:n]); err != nil {
				return false, "", fmt.Errorf("failed to write chunk data: %w", err)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return false, "", fmt.Errorf("failed to read stream chunk: %w", readErr)
		}
	}

	// Write 4-byte 0 to terminate stream
	binary.BigEndian.PutUint32(chunkLenBuf, 0)
	if _, err := conn.Write(chunkLenBuf); err != nil {
		return false, "", fmt.Errorf("failed to write stream terminator: %w", err)
	}

	// Read response (supporting null-delimited or newline-delimited)
	respReader := bufio.NewReader(conn)
	respBytes, err := respReader.ReadBytes('\x00')
	if err != nil && len(respBytes) == 0 {
		line, lineErr := respReader.ReadString('\n')
		if lineErr != nil && len(line) == 0 {
			return false, "", fmt.Errorf("failed to read clamav response: %w", err)
		}
		respBytes = []byte(line)
	}

	resp := strings.TrimSpace(string(bytes.Trim(respBytes, "\x00\r\n")))
	if resp == "stream: OK" {
		return true, "", nil
	}

	if strings.HasPrefix(resp, "stream: ") && strings.HasSuffix(resp, " FOUND") {
		virus := strings.TrimPrefix(resp, "stream: ")
		virus = strings.TrimSuffix(virus, " FOUND")
		virus = strings.TrimSpace(virus)
		return false, virus, fmt.Errorf("%w: %s", ErrVirusDetected, virus)
	}

	return false, "", fmt.Errorf("clamav unexpected response: %s", resp)
}
