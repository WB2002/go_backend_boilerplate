package middleware

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestMaxBodySize_RejectsOversizedPayload(t *testing.T) {
	router := gin.New()
	router.Use(MaxBodySize(1024)) // 1 KB
	router.POST("/upload", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		c.String(http.StatusOK, "read %d bytes", len(body))
	})

	oversized := bytes.Repeat([]byte("a"), 2048)
	w := performRequest(router, "POST", "/upload", string(oversized), nil)
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

func TestMaxBodySize_AllowsWithinLimit(t *testing.T) {
	router := gin.New()
	router.Use(MaxBodySize(1024))
	router.POST("/upload", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		c.String(http.StatusOK, "read %d bytes", len(body))
	})

	payload := strings.Repeat("a", 512)
	w := performRequest(router, "POST", "/upload", payload, nil)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "read 512 bytes", w.Body.String())
}

func TestMaxBodySize_RejectsChunkedStreamExceedingLimit(t *testing.T) {
	router := gin.New()
	router.Use(MaxBodySize(1024))
	router.POST("/upload", func(c *gin.Context) {
		_, err := io.ReadAll(c.Request.Body)
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				c.AbortWithStatus(http.StatusRequestEntityTooLarge)
				return
			}
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		c.Status(http.StatusOK)
	})

	oversized := strings.Repeat("b", 2048)
	req, _ := http.NewRequest(http.MethodPost, "/upload", io.NopCloser(strings.NewReader(oversized)))
	req.ContentLength = -1 // simulate chunked encoding with unknown Content-Length

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

func TestMaxBodySize_DefaultLimitWhenNonPositive(t *testing.T) {
	router := gin.New()
	router.Use(MaxBodySize(0))
	router.POST("/upload", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		c.String(http.StatusOK, "read %d bytes", len(body))
	})

	withinDefault := strings.Repeat("c", 1024)
	wOK := performRequest(router, "POST", "/upload", withinDefault, nil)
	assert.Equal(t, http.StatusOK, wOK.Code)

	overDefault := strings.Repeat("c", (1<<20)+1)
	wTooLarge := performRequest(router, "POST", "/upload", overDefault, nil)
	assert.Equal(t, http.StatusRequestEntityTooLarge, wTooLarge.Code)
}
