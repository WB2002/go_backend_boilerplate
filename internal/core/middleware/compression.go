package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type gzipWriter struct {
	gin.ResponseWriter
	threshold    int
	buf          bytes.Buffer
	gz           *gzip.Writer
	isGzip       bool
	status       int
	headerCalled bool
}

func newGzipWriter(w gin.ResponseWriter, threshold int) *gzipWriter {
	return &gzipWriter{
		ResponseWriter: w,
		threshold:      threshold,
		status:         http.StatusOK,
	}
}

func (gw *gzipWriter) WriteHeader(code int) {
	gw.status = code
	gw.headerCalled = true
	// Delay calling underlying WriteHeader until body threshold is evaluated
}

func (gw *gzipWriter) Status() int {
	if gw.headerCalled {
		return gw.status
	}
	return gw.ResponseWriter.Status()
}

func (gw *gzipWriter) Written() bool {
	return gw.headerCalled || gw.ResponseWriter.Written()
}

func (gw *gzipWriter) Size() int {
	if gw.isGzip {
		return gw.ResponseWriter.Size()
	}
	underlyingSize := gw.ResponseWriter.Size()
	if underlyingSize < 0 {
		underlyingSize = 0
	}
	return gw.buf.Len() + underlyingSize
}

func (gw *gzipWriter) WriteHeaderNow() {
	if gw.Written() && !gw.ResponseWriter.Written() {
		if gw.buf.Len() >= gw.threshold {
			_ = gw.startGzip()
		} else {
			gw.ResponseWriter.WriteHeader(gw.status)
			gw.headerCalled = false
		}
	}
}

func (gw *gzipWriter) Write(data []byte) (int, error) {
	if gw.Header().Get("Content-Encoding") != "" {
		// Already encoded, pass directly
		if gw.headerCalled {
			gw.ResponseWriter.WriteHeader(gw.status)
			gw.headerCalled = false
		}
		return gw.ResponseWriter.Write(data)
	}

	if gw.isGzip {
		return gw.gz.Write(data)
	}

	// Buffer until threshold
	gw.buf.Write(data)
	if gw.buf.Len() >= gw.threshold {
		if err := gw.startGzip(); err != nil {
			return 0, err
		}
	}
	return len(data), nil
}

func (gw *gzipWriter) WriteString(s string) (int, error) {
	return gw.Write([]byte(s))
}

func (gw *gzipWriter) startGzip() error {
	gw.isGzip = true
	gw.Header().Set("Content-Encoding", "gzip")
	gw.Header().Add("Vary", "Accept-Encoding")
	gw.Header().Del("Content-Length")

	gw.ResponseWriter.WriteHeader(gw.status)
	gw.headerCalled = false

	gw.gz = gzip.NewWriter(gw.ResponseWriter)
	if gw.buf.Len() > 0 {
		_, err := gw.gz.Write(gw.buf.Bytes())
		gw.buf.Reset()
		return err
	}
	return nil
}

func (gw *gzipWriter) finish() error {
	if gw.isGzip {
		if gw.gz != nil {
			return gw.gz.Close()
		}
		return nil
	}

	// If not gzipped, flush buffer directly
	if gw.headerCalled {
		gw.ResponseWriter.WriteHeader(gw.status)
		gw.headerCalled = false
	}
	if gw.buf.Len() > 0 {
		_, err := gw.ResponseWriter.Write(gw.buf.Bytes())
		gw.buf.Reset()
		return err
	}
	return nil
}

// GzipCompression conditionally compresses HTTP response payloads exceeding minSizeThreshold.
func GzipCompression(minSizeThreshold int) gin.HandlerFunc {
	if minSizeThreshold <= 0 {
		minSizeThreshold = 1024
	}

	return func(c *gin.Context) {
		if !strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
			c.Next()
			return
		}

		gw := newGzipWriter(c.Writer, minSizeThreshold)
		c.Writer = gw
		defer func() {
			_ = gw.finish()
		}()

		c.Next()
	}
}

// Ensure gzipWriter satisfies gin.ResponseWriter and io.Writer
var _ gin.ResponseWriter = (*gzipWriter)(nil)
var _ io.Writer = (*gzipWriter)(nil)
