package email

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSMTPMailer_HonorsContextCancellation(t *testing.T) {
	mailer := NewSMTPMailer(SMTPConfig{
		Host: "192.0.2.1", // non-routable TEST-NET-1 IP
		Port: 2525,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := mailer.Send(ctx, Message{
		To:       "user@example.com",
		Subject:  "Test",
		BodyText: "Hello",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "context")
}

func TestSMTPMailer_HonorsContextDeadlineDuringSession(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()

	tcpAddr := ln.Addr().(*net.TCPAddr)
	mailer := NewSMTPMailer(SMTPConfig{
		Host: tcpAddr.IP.String(),
		Port: tcpAddr.Port,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = mailer.Send(ctx, Message{
		To:       "user@example.com",
		Subject:  "Slow Server Test",
		BodyText: "Hello",
	})
	elapsed := time.Since(start)

	if conn := <-accepted; conn != nil {
		_ = conn.Close()
	}

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "context")
	assert.Less(t, elapsed, time.Second)
}

func TestSMTPMailer_StripsCRLFInHeadersAndSendsBody(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	receivedCh := make(chan string, 1)
	rcptCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			errCh <- acceptErr
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		writeLine := func(line string) {
			_, _ = fmt.Fprintf(conn, "%s\r\n", line)
		}

		writeLine("220 localhost ESMTP test")
		var dataLines []string
		inData := false

		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				errCh <- readErr
				return
			}
			trimmed := strings.TrimRight(line, "\r\n")

			if inData {
				if trimmed == "." {
					writeLine("250 2.0.0 OK")
					inData = false
					receivedCh <- strings.Join(dataLines, "\n")
					continue
				}
				dataLines = append(dataLines, trimmed)
				continue
			}

			upper := strings.ToUpper(trimmed)
			switch {
			case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
				writeLine("250-localhost")
				writeLine("250 OK")
			case strings.HasPrefix(upper, "MAIL FROM:"):
				writeLine("250 2.1.0 OK")
			case strings.HasPrefix(upper, "RCPT TO:"):
				rcptCh <- trimmed
				writeLine("250 2.1.5 OK")
			case upper == "DATA":
				writeLine("354 End data with <CR><LF>.<CR><LF>")
				inData = true
			case upper == "QUIT":
				writeLine("221 2.0.0 Bye")
				return
			default:
				writeLine("250 OK")
			}
		}
	}()

	tcpAddr := ln.Addr().(*net.TCPAddr)
	mailer := NewSMTPMailer(SMTPConfig{
		Host: tcpAddr.IP.String(),
		Port: tcpAddr.Port,
		From: "noreply@example.com\r\nX-Injected-From: evil",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = mailer.Send(ctx, Message{
		To:       "user@example.com\r\nBcc: attacker@evil.com",
		Subject:  "Reset\r\nBcc: attacker@evil.com",
		BodyHTML: "<p>Reset token: abc</p>",
	})
	require.NoError(t, err)

	select {
	case srvErr := <-errCh:
		require.NoError(t, srvErr)
	default:
	}

	rcpt := <-rcptCh
	assert.NotContains(t, rcpt, "\r")
	assert.NotContains(t, rcpt, "\n")

	rawData := <-receivedCh
	assert.Contains(t, rawData, "<p>Reset token: abc</p>")
	for _, line := range strings.Split(rawData, "\n") {
		assert.False(t, strings.HasPrefix(line, "Bcc:"), "unexpected injected header line: %q", line)
		assert.False(t, strings.HasPrefix(line, "X-Injected-From:"), "unexpected injected header line: %q", line)
	}
}
