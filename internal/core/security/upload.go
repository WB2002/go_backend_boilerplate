package security

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"go_backend_boilerplate/internal/config"
)

var (
	// ErrFileTooLarge is returned when an uploaded file exceeds the configured individual file size threshold.
	ErrFileTooLarge = errors.New("file size exceeds maximum allowed size")
	// ErrTotalSizeTooLarge is returned when total multipart form files exceed the maximum total threshold.
	ErrTotalSizeTooLarge = errors.New("total upload size exceeds maximum allowed size")
	// ErrInvalidMIME is returned when magic-byte inspection reveals an disallowed or spoofed MIME type.
	ErrInvalidMIME = errors.New("invalid file signature / magic bytes")
	// ErrEmptyFile is returned when an uploaded file is 0 bytes.
	ErrEmptyFile = errors.New("file is empty")
)

// UploadConfig specifies size thresholds and permitted MIME types for file uploads.
type UploadConfig struct {
	MaxFileSize  int64
	MaxTotalSize int64
	AllowedMIMEs []string
}

// ProcessedFile contains metadata and verified binary content of an uploaded file.
type ProcessedFile struct {
	Filename string
	Size     int64
	MIMEType string
	Content  []byte
}

// UploadService coordinates file inspection, size validation, path sanitization, and virus scanning.
type UploadService struct {
	cfg     UploadConfig
	scanner *ClamAVScanner
}

// NewUploadService constructs an UploadService with the specified config and optional ClamAVScanner.
func NewUploadService(cfg UploadConfig, scanner *ClamAVScanner) *UploadService {
	if cfg.MaxFileSize <= 0 {
		cfg.MaxFileSize = 5 * 1024 * 1024 // 5MB
	}
	if cfg.MaxTotalSize <= 0 {
		cfg.MaxTotalSize = 20 * 1024 * 1024 // 20MB
	}
	return &UploadService{
		cfg:     cfg,
		scanner: scanner,
	}
}

// NewUploadServiceFromConfig creates an UploadService using SecurityConfig and allowed MIME types.
func NewUploadServiceFromConfig(cfg config.SecurityConfig, allowedMIMEs []string, scanner *ClamAVScanner) *UploadService {
	return NewUploadService(UploadConfig{
		MaxFileSize:  cfg.UploadMaxFileSize,
		MaxTotalSize: cfg.UploadMaxTotalSize,
		AllowedMIMEs: allowedMIMEs,
	}, scanner)
}

// SanitizeFilename strips directory traversal sequences, null bytes, control characters,
// and path separators, returning a safe base file name truncated to 255 bytes max.
func SanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\x00", "")
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = filepath.Base(name)
	name = strings.Trim(name, ". \t\r\n")

	// Strip control characters
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)

	// Truncate to 255 bytes while preserving extension if present
	if len(name) > 255 {
		ext := filepath.Ext(name)
		if len(ext) < 255 {
			base := name[:255-len(ext)]
			name = base + ext
		} else {
			name = name[:255]
		}
	}

	if name == "" || name == "." || name == "/" {
		return "uploaded_file"
	}
	return name
}

// ProcessFile validates file size, inspects magic bytes for MIME spoofing, cleans file names,
// and streams through ClamAV if configured using context.Background.
func (s *UploadService) ProcessFile(file *multipart.FileHeader) (*ProcessedFile, error) {
	return s.ProcessFileWithContext(context.Background(), file)
}

// ProcessFileWithContext validates file size, inspects magic bytes for MIME spoofing, cleans file names,
// and streams through ClamAV if configured with context propagation.
func (s *UploadService) ProcessFileWithContext(ctx context.Context, file *multipart.FileHeader) (*ProcessedFile, error) {
	if file == nil {
		return nil, errors.New("file header is nil")
	}

	if s.cfg.MaxFileSize > 0 && file.Size > s.cfg.MaxFileSize {
		return nil, fmt.Errorf("%w: %d bytes (max: %d bytes)", ErrFileTooLarge, file.Size, s.cfg.MaxFileSize)
	}

	src, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to open uploaded file: %w", err)
	}
	defer func() { _ = src.Close() }()

	limit := s.cfg.MaxFileSize
	if limit <= 0 {
		limit = 5 * 1024 * 1024
	}

	content, err := io.ReadAll(io.LimitReader(src, limit+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read uploaded file: %w", err)
	}

	if int64(len(content)) > limit {
		return nil, fmt.Errorf("%w: %d bytes (max: %d bytes)", ErrFileTooLarge, len(content), limit)
	}

	if len(content) == 0 {
		return nil, ErrEmptyFile
	}

	// Detect MIME via magic bytes
	headerBytes := content
	if len(headerBytes) > 512 {
		headerBytes = headerBytes[:512]
	}
	detectedRaw := http.DetectContentType(headerBytes)
	detectedMIME := strings.ToLower(strings.TrimSpace(strings.Split(detectedRaw, ";")[0]))

	declaredRaw := file.Header.Get("Content-Type")
	declaredMIME := strings.ToLower(strings.TrimSpace(strings.Split(declaredRaw, ";")[0]))

	// Check allowed MIMEs
	if len(s.cfg.AllowedMIMEs) > 0 {
		allowed := false
		for _, m := range s.cfg.AllowedMIMEs {
			baseM := strings.ToLower(strings.TrimSpace(strings.Split(m, ";")[0]))
			if baseM == detectedMIME {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("%w: detected MIME %s is not allowed", ErrInvalidMIME, detectedMIME)
		}
	}

	// Check declared vs detected MIME (when declared MIME is specified and non-generic)
	if declaredMIME != "" && declaredMIME != "application/octet-stream" {
		if declaredMIME != detectedMIME {
			return nil, fmt.Errorf("%w: declared MIME %s does not match detected MIME %s", ErrInvalidMIME, declaredMIME, detectedMIME)
		}
	}

	// Filename sanitization
	safeFilename := SanitizeFilename(file.Filename)

	// Stream through ClamAV scanner if configured
	if s.scanner != nil {
		clean, virus, scanErr := s.scanner.ScanStream(ctx, bytes.NewReader(content))
		if scanErr != nil {
			return nil, scanErr
		}
		if !clean {
			return nil, fmt.Errorf("%w: %s", ErrVirusDetected, virus)
		}
	}

	return &ProcessedFile{
		Filename: safeFilename,
		Size:     int64(len(content)),
		MIMEType: detectedMIME,
		Content:  content,
	}, nil
}

// ValidateMultipart checks individual and cumulative file sizes in a multipart form before full processing.
func (s *UploadService) ValidateMultipart(form *multipart.Form) error {
	if form == nil || form.File == nil {
		return nil
	}

	var totalSize int64
	for _, fileHeaders := range form.File {
		for _, fh := range fileHeaders {
			if s.cfg.MaxFileSize > 0 && fh.Size > s.cfg.MaxFileSize {
				return fmt.Errorf("%w: file %s size %d exceeds max %d", ErrFileTooLarge, fh.Filename, fh.Size, s.cfg.MaxFileSize)
			}
			totalSize += fh.Size
			if s.cfg.MaxTotalSize > 0 && totalSize > s.cfg.MaxTotalSize {
				return fmt.Errorf("%w: total size %d exceeds max %d", ErrTotalSizeTooLarge, totalSize, s.cfg.MaxTotalSize)
			}
		}
	}

	return nil
}
