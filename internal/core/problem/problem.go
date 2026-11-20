package problem

import (
	"fmt"

	"github.com/gin-gonic/gin"
)

// ContentTypeProblemJSON is the MIME type for RFC 7807 problem details.
const ContentTypeProblemJSON = "application/problem+json; charset=utf-8"

// InvalidParam describes a specific invalid query or body parameter.
type InvalidParam struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
	Code   string `json:"code,omitempty"`
}

// Details models an RFC 7807 problem document with business extension codes.
type Details struct {
	Type          string         `json:"type,omitempty"`
	Title         string         `json:"title"`
	Status        int            `json:"status"`
	Detail        string         `json:"detail,omitempty"`
	Instance      string         `json:"instance,omitempty"`
	Code          string         `json:"code,omitempty"`
	Error         string         `json:"error,omitempty"`
	InvalidParams []InvalidParam `json:"invalid_params,omitempty"`
}

// New creates a new Details problem object.
func New(status int, code, title, detail, instance string) *Details {
	return &Details{
		Type:     fmt.Sprintf("https://httpstatuses.io/%d", status),
		Title:    title,
		Status:   status,
		Detail:   detail,
		Instance: instance,
		Code:     code,
		Error:    title,
	}
}

// Respond writes an RFC 7807 response to the Gin context.
func Respond(c *gin.Context, status int, code, title, detail string) {
	instance := ""
	if c.Request != nil && c.Request.URL != nil {
		instance = c.Request.URL.Path
	}
	prob := New(status, code, title, detail, instance)
	c.Header("Content-Type", ContentTypeProblemJSON)
	c.AbortWithStatusJSON(status, prob)
}
