package problem

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type SampleRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8"`
}

func TestProblemDetailsFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	r.GET("/test-error", func(c *gin.Context) {
		Respond(c, http.StatusNotFound, "USER_NOT_FOUND", "User Not Found", "The user with id 123 was not found")
	})

	r.POST("/test-validation", func(c *gin.Context) {
		var req SampleRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			RespondValidation(c, err)
			return
		}
		c.Status(http.StatusOK)
	})

	// 1. Test Standard Problem Details
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test-error", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, ContentTypeProblemJSON, w.Header().Get("Content-Type"))

	var p Details
	err := json.Unmarshal(w.Body.Bytes(), &p)
	require.NoError(t, err)
	assert.Equal(t, "USER_NOT_FOUND", p.Code)
	assert.Equal(t, "User Not Found", p.Title)
	assert.Equal(t, http.StatusNotFound, p.Status)
	assert.Equal(t, "The user with id 123 was not found", p.Detail)

	// 2. Test Validation Error Mapping
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("POST", "/test-validation", strings.NewReader(`{"email":"bad-email","password":"short"}`))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusBadRequest, w2.Code)
	var valProblem Details
	err = json.Unmarshal(w2.Body.Bytes(), &valProblem)
	require.NoError(t, err)
	assert.Equal(t, "VALIDATION_FAILED", valProblem.Code)
	assert.Len(t, valProblem.InvalidParams, 2)
	assert.Equal(t, "email", valProblem.InvalidParams[0].Field)
	assert.Equal(t, "password", valProblem.InvalidParams[1].Field)
}
