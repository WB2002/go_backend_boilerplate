package security_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oschwald/geoip2-golang"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/core/security"
)

// --- Upload & Magic-Byte Tests ---

func createMultipartFile(t *testing.T, fieldName, fileName, contentType string, content []byte) *multipart.FileHeader {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	h := make(map[string][]string)
	h["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fieldName, fileName)}
	if contentType != "" {
		h["Content-Type"] = []string{contentType}
	}

	part, err := writer.CreatePart(h)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	reader := multipart.NewReader(body, writer.Boundary())
	form, err := reader.ReadForm(int64(body.Len() + 1024))
	require.NoError(t, err)
	require.NotEmpty(t, form.File[fieldName])

	return form.File[fieldName][0]
}

func TestMagicByteInspectionRejectsSpoofedMIME(t *testing.T) {
	uploader := security.NewUploadService(security.UploadConfig{
		MaxFileSize:  5 * 1024 * 1024,
		MaxTotalSize: 20 * 1024 * 1024,
		AllowedMIMEs: []string{"image/png", "image/jpeg"},
	}, nil)

	// Create fake file named photo.png containing shell script text
	fileHeader := createMultipartFile(t, "file", "photo.png", "image/png", []byte("#!/bin/bash\necho 'malicious'"))

	_, err := uploader.ProcessFile(fileHeader)
	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid file signature / magic bytes")
}

func TestUploadValidFileSignaturePasses(t *testing.T) {
	uploader := security.NewUploadService(security.UploadConfig{
		MaxFileSize:  5 * 1024 * 1024,
		MaxTotalSize: 20 * 1024 * 1024,
		AllowedMIMEs: []string{"image/png"},
	}, nil)

	// Valid PNG magic bytes (\x89PNG\r\n\x1a\n) + dummy chunk
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R'}
	fileHeader := createMultipartFile(t, "file", "valid.png", "image/png", pngData)

	res, err := uploader.ProcessFile(fileHeader)
	require.NoError(t, err)
	assert.Equal(t, "valid.png", res.Filename)
	assert.Equal(t, int64(len(pngData)), res.Size)
	assert.Equal(t, "image/png", res.MIMEType)
	assert.Equal(t, pngData, res.Content)
}

func TestUploadFileSizeLimitExceeded(t *testing.T) {
	uploader := security.NewUploadService(security.UploadConfig{
		MaxFileSize:  100,
		MaxTotalSize: 1000,
		AllowedMIMEs: []string{"text/plain"},
	}, nil)

	largeContent := make([]byte, 200)
	fileHeader := createMultipartFile(t, "file", "large.txt", "text/plain", largeContent)

	_, err := uploader.ProcessFile(fileHeader)
	require.Error(t, err)
	assert.ErrorIs(t, err, security.ErrFileTooLarge)
}

func TestValidateMultipartTotalSizeLimit(t *testing.T) {
	uploader := security.NewUploadService(security.UploadConfig{
		MaxFileSize:  500,
		MaxTotalSize: 800,
		AllowedMIMEs: []string{"text/plain"},
	}, nil)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for i := 0; i < 3; i++ {
		p, err := writer.CreateFormFile(fmt.Sprintf("file%d", i), fmt.Sprintf("file%d.txt", i))
		require.NoError(t, err)
		_, err = p.Write(make([]byte, 300))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())

	reader := multipart.NewReader(body, writer.Boundary())
	form, err := reader.ReadForm(2000)
	require.NoError(t, err)

	err = uploader.ValidateMultipart(form)
	require.Error(t, err)
	assert.ErrorIs(t, err, security.ErrTotalSizeTooLarge)
}

func TestFilenameSanitization(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"../../etc/passwd", "passwd"},
		{"..\\..\\windows\\system32\\cmd.exe", "cmd.exe"},
		{"/root/.ssh/id_rsa", "id_rsa"},
		{"nested/path/to/my_image.png", "my_image.png"},
		{"evil\x00payload.pdf", "evilpayload.pdf"},
		{"../../../", "uploaded_file"},
		{"", "uploaded_file"},
		{"normal-file.txt", "normal-file.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, security.SanitizeFilename(tt.input))
		})
	}
}

// --- ClamAV Scanner Tests ---

func startMockClamAVServer(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	done := make(chan struct{})

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-done:
					return
				default:
					return
				}
			}

			go func(c net.Conn) {
				defer func() { _ = c.Close() }()

				// Read command "zINSTREAM\x00"
				cmdBuf := make([]byte, 10)
				_, err := io.ReadFull(c, cmdBuf)
				if err != nil || string(cmdBuf) != "zINSTREAM\x00" {
					return
				}

				// Read chunks
				var totalData bytes.Buffer
				lenBuf := make([]byte, 4)
				for {
					if _, err := io.ReadFull(c, lenBuf); err != nil {
						return
					}
					chunkLen := binary.BigEndian.Uint32(lenBuf)
					if chunkLen == 0 {
						break
					}
					chunk := make([]byte, chunkLen)
					if _, err := io.ReadFull(c, chunk); err != nil {
						return
					}
					totalData.Write(chunk)
				}

				// Check content
				if bytes.Contains(totalData.Bytes(), []byte("EICAR")) {
					_, _ = c.Write([]byte("stream: Win.Test.EICAR_HDB-1 FOUND\x00"))
				} else {
					_, _ = c.Write([]byte("stream: OK\x00"))
				}
			}(conn)
		}
	}()

	cleanup := func() {
		close(done)
		_ = listener.Close()
	}

	return listener.Addr().String(), cleanup
}

func TestClamAVScanStreamCleanAndInfected(t *testing.T) {
	addr, cleanup := startMockClamAVServer(t)
	defer cleanup()

	scanner := security.NewClamAVScanner(addr, 2*time.Second)

	// Clean stream
	clean, virus, err := scanner.ScanStream(context.Background(), strings.NewReader("hello harmless clean text"))
	assert.NoError(t, err)
	assert.True(t, clean)
	assert.Empty(t, virus)

	// Infected stream with EICAR
	clean, virus, err = scanner.ScanStream(context.Background(), strings.NewReader("X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*"))
	require.Error(t, err)
	assert.ErrorIs(t, err, security.ErrVirusDetected)
	assert.False(t, clean)
	assert.Equal(t, "Win.Test.EICAR_HDB-1", virus)
}

func TestClamAVConnectionFailure(t *testing.T) {
	// Point to closed port
	scanner := security.NewClamAVScanner("127.0.0.1:54321", 100*time.Millisecond)

	clean, virus, err := scanner.ScanStream(context.Background(), strings.NewReader("test"))
	require.Error(t, err)
	assert.ErrorIs(t, err, security.ErrClamAVUnavailable)
	assert.False(t, clean)
	assert.Empty(t, virus)
}

func TestUploadWithClamAVIntegrationRejectsVirus(t *testing.T) {
	addr, cleanup := startMockClamAVServer(t)
	defer cleanup()

	scanner := security.NewClamAVScanner(addr, 2*time.Second)
	uploader := security.NewUploadService(security.UploadConfig{
		MaxFileSize:  5 * 1024 * 1024,
		MaxTotalSize: 20 * 1024 * 1024,
		AllowedMIMEs: []string{"text/plain"},
	}, scanner)

	eicarFile := createMultipartFile(t, "file", "eicar.txt", "text/plain", []byte("sample containing EICAR string"))
	_, err := uploader.ProcessFile(eicarFile)
	require.Error(t, err)
	assert.ErrorIs(t, err, security.ErrVirusDetected)
}

// --- hCaptcha Verification Tests ---

func TestHCaptchaVerification(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

		require.NoError(t, r.ParseForm())
		secret := r.FormValue("secret")
		token := r.FormValue("response")
		clientIP := r.FormValue("remoteip")

		assert.Equal(t, "my-secret-key", secret)
		assert.Equal(t, "192.0.2.1", clientIP)

		if token == "valid-token" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(security.HCaptchaResponse{Success: true})
			return
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(security.HCaptchaResponse{
			Success:    false,
			ErrorCodes: []string{"invalid-or-already-seen-response"},
		})
	}))
	defer ts.Close()

	verifier := security.NewCaptchaVerifier("my-secret-key", security.WithCaptchaVerifyURL(ts.URL))

	// Valid token
	success, err := verifier.VerifyToken(context.Background(), "valid-token", "192.0.2.1")
	require.NoError(t, err)
	assert.True(t, success)

	// Invalid token
	success, err = verifier.VerifyToken(context.Background(), "bad-token", "192.0.2.1")
	require.NoError(t, err)
	assert.False(t, success)

	// Empty token
	_, err = verifier.VerifyToken(context.Background(), "", "192.0.2.1")
	require.ErrorIs(t, err, security.ErrEmptyCaptchaToken)
}

// --- GeoIP Filter & Private IP Exemption Tests ---

type mockGeoIPReader struct {
	countries map[string]string
}

func (m *mockGeoIPReader) Country(ip net.IP) (*geoip2.Country, error) {
	record := &geoip2.Country{}
	if iso, exists := m.countries[ip.String()]; exists {
		record.Country.IsoCode = iso
		return record, nil
	}
	record.Country.IsoCode = "ZZ"
	return record, nil
}

func (m *mockGeoIPReader) Close() error {
	return nil
}

func TestGeoIPAllowAndBlock(t *testing.T) {
	mockReader := &mockGeoIPReader{
		countries: map[string]string{
			"203.0.113.1": "US", // Allowed
			"203.0.113.2": "CA", // Allowed
			"203.0.113.3": "CN", // Blocked
		},
	}

	svc := security.NewGeoIPServiceWithReader(mockReader, []string{"US", "CA"})
	defer svc.Close()

	// Allowed country
	allowed, err := svc.IsAllowed("203.0.113.1")
	require.NoError(t, err)
	assert.True(t, allowed)

	// Blocked country
	allowed, err = svc.IsAllowed("203.0.113.3")
	require.NoError(t, err)
	assert.False(t, allowed)

	// Private and Loopback IP exemptions (always allowed)
	privateIPs := []string{
		"127.0.0.1",
		"::1",
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.100",
		"169.254.1.1",
		"::ffff:127.0.0.1",
	}
	for _, pip := range privateIPs {
		allowed, err := svc.IsAllowed(pip)
		require.NoError(t, err, "IP: %s", pip)
		assert.True(t, allowed, "Private IP %s must be exempted", pip)
	}
}

func TestGeoIPFailOpenWhenDBMissingOrEmpty(t *testing.T) {
	// Missing file -> fail-open
	svcMissing, err := security.NewGeoIPService("non_existent_file.mmdb", []string{"US"})
	require.NoError(t, err)
	allowed, err := svcMissing.IsAllowed("203.0.113.1")
	require.NoError(t, err)
	assert.True(t, allowed, "Must fail open when database does not exist")

	// Empty file -> fail-open
	tmpDir := t.TempDir()
	emptyFile := filepath.Join(tmpDir, "empty.mmdb")
	require.NoError(t, os.WriteFile(emptyFile, []byte{}, 0644))

	svcEmpty, err := security.NewGeoIPService(emptyFile, []string{"US"})
	require.NoError(t, err)
	allowed, err = svcEmpty.IsAllowed("203.0.113.1")
	require.NoError(t, err)
	assert.True(t, allowed, "Must fail open when database is empty")
}

func TestGeoIPFilterMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockReader := &mockGeoIPReader{
		countries: map[string]string{
			"198.51.100.1": "US",
			"198.51.100.2": "FR",
		},
	}
	svc := security.NewGeoIPServiceWithReader(mockReader, []string{"US"})

	r := gin.New()
	r.Use(security.GeoIPFilter(svc))
	r.GET("/secure", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Allowed IP (US)
	reqAllowed := httptest.NewRequest(http.MethodGet, "/secure", nil)
	reqAllowed.Header.Set("X-Forwarded-For", "198.51.100.1")
	wAllowed := httptest.NewRecorder()
	r.ServeHTTP(wAllowed, reqAllowed)
	assert.Equal(t, http.StatusOK, wAllowed.Code)

	// Blocked IP (FR)
	reqBlocked := httptest.NewRequest(http.MethodGet, "/secure", nil)
	reqBlocked.Header.Set("X-Forwarded-For", "198.51.100.2")
	wBlocked := httptest.NewRecorder()
	r.ServeHTTP(wBlocked, reqBlocked)
	assert.Equal(t, http.StatusForbidden, wBlocked.Code)
	assert.Contains(t, wBlocked.Body.String(), "country not allowed")

	// Loopback / localhost (exempted)
	reqLocal := httptest.NewRequest(http.MethodGet, "/secure", nil)
	reqLocal.Header.Set("X-Forwarded-For", "127.0.0.1")
	wLocal := httptest.NewRecorder()
	r.ServeHTTP(wLocal, reqLocal)
	assert.Equal(t, http.StatusOK, wLocal.Code)
}

func TestConstructorsFromConfig(t *testing.T) {
	cfg := config.SecurityConfig{
		GeoIPDBPath:           "",
		GeoIPAllowedCountries: []string{"US"},
		UploadMaxFileSize:     1024,
		UploadMaxTotalSize:    4096,
		ClamAVAddr:            "localhost:3310",
		HCaptchaSecret:        "sec",
	}

	geoSvc, err := security.NewGeoIPServiceFromConfig(cfg)
	require.NoError(t, err)
	assert.NotNil(t, geoSvc)

	clamScanner := security.NewClamAVScannerFromConfig(cfg, time.Second)
	assert.NotNil(t, clamScanner)

	uploadSvc := security.NewUploadServiceFromConfig(cfg, []string{"text/plain"}, clamScanner)
	assert.NotNil(t, uploadSvc)

	captchaVerifier := security.NewCaptchaVerifierFromConfig(cfg)
	assert.NotNil(t, captchaVerifier)
}
