package problem

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

// RespondValidation inspects binding errors and returns an RFC 7807 400 Bad Request response.
func RespondValidation(c *gin.Context, err error) {
	instance := ""
	if c.Request != nil && c.Request.URL != nil {
		instance = c.Request.URL.Path
	}

	var ve validator.ValidationErrors
	if errors.As(err, &ve) {
		invalidParams := make([]InvalidParam, 0, len(ve))
		for _, fe := range ve {
			field := toSnakeOrLower(fe.Field())
			reason := formatValidationReason(fe)
			invalidParams = append(invalidParams, InvalidParam{
				Field:  field,
				Reason: reason,
				Code:   fe.Tag(),
			})
		}

		prob := &Details{
			Type:          "https://httpstatuses.io/400",
			Title:         "Validation Failed",
			Status:        http.StatusBadRequest,
			Detail:        "One or more request parameters failed validation.",
			Instance:      instance,
			Code:          "VALIDATION_FAILED",
			InvalidParams: invalidParams,
		}

		c.Header("Content-Type", ContentTypeProblemJSON)
		c.AbortWithStatusJSON(http.StatusBadRequest, prob)
		return
	}

	// Fallback for malformed JSON or unmarshal errors
	Respond(c, http.StatusBadRequest, "MALFORMED_REQUEST", "Malformed Request", "Unable to parse request payload as valid JSON.")
}

func formatValidationReason(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "This field is required."
	case "email":
		return "Must be a valid email address."
	case "min":
		return fmt.Sprintf("Must be at least %s characters or value.", fe.Param())
	case "max":
		return fmt.Sprintf("Must not exceed %s characters or value.", fe.Param())
	case "uuid":
		return "Must be a valid UUID."
	default:
		return fmt.Sprintf("Failed validation constraint %q.", fe.Tag())
	}
}

func toSnakeOrLower(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteRune('_')
			}
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
