package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestAuditContextMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AuditContext())

	var capturedIP string
	var capturedUA string

	r.GET("/test", func(c *gin.Context) {
		capturedIP = GetClientIP(c)
		capturedUA = GetUserAgent(c)
		c.Status(http.StatusOK)
	})

	// 1. With X-Forwarded-For header
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.195, 10.0.0.1")
	req.Header.Set("User-Agent", "Mozilla/5.0 AuditBot")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "203.0.113.195", capturedIP)
	assert.Equal(t, "Mozilla/5.0 AuditBot", capturedUA)

	// 2. With X-Real-IP header
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.Header.Set("X-Real-IP", "198.51.100.42")
	req2.Header.Set("User-Agent", "Curl/7.68")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, "198.51.100.42", capturedIP)
	assert.Equal(t, "Curl/7.68", capturedUA)
}
