package middleware

import (
	"github.com/gin-gonic/gin"
	"go_backend_boilerplate/internal/core/overload"
)

// InboundOverload wraps the OverloadManager into a Gin middleware.
func InboundOverload(mgr *overload.Manager) gin.HandlerFunc {
	return mgr.Middleware()
}

// WithPriority annotates a route with the given priority level.
func WithPriority(priority overload.Priority) gin.HandlerFunc {
	return overload.WithPriority(priority)
}
