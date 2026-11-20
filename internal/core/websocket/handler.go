package websocket

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"go_backend_boilerplate/internal/core/auth"
)

// Handler manages WebSocket upgrade handshakes, authentication, and connection binding.
type Handler struct {
	hub          *Hub
	tokenManager auth.TokenManager
	upgrader     websocket.Upgrader
	env          string
}

// NewHandler constructs a WebSocket HTTP handler.
func NewHandler(hub *Hub, tokenManager auth.TokenManager, allowedOrigins ...string) *Handler {
	origins := allowedOrigins
	env := ""
	if hub != nil {
		env = hub.config.Env
		if len(origins) == 0 && len(hub.config.AllowedOrigins) > 0 {
			origins = hub.config.AllowedOrigins
		}
	}

	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return checkOrigin(r, origins)
		},
	}

	return &Handler{
		hub:          hub,
		tokenManager: tokenManager,
		upgrader:     upgrader,
		env:          env,
	}
}

// SetEnv sets the runtime environment name used to enforce production auth rules.
func (h *Handler) SetEnv(env string) {
	h.env = env
}

// SetUpgrader overrides the default Gorilla WebSocket upgrader.
func (h *Handler) SetUpgrader(u websocket.Upgrader) {
	h.upgrader = u
}

// RegisterRoutes registers the WebSocket endpoint on the provided Gin router.
func (h *Handler) RegisterRoutes(r gin.IRoutes, path ...string) {
	routePath := "/realtime"
	if len(path) > 0 && path[0] != "" {
		routePath = path[0]
	}
	r.GET(routePath, h.HandleWebSocket)
}

// HandleWebSocket handles the HTTP upgrade to WebSocket, validates authentication tokens,
// and initiates the read/write pumps.
func (h *Handler) HandleWebSocket(c *gin.Context) {
	if h.tokenManager == nil {
		log.Error().Msg("websocket handshake rejected: token manager is nil")
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "authentication service unavailable"})
		return
	}

	isProduction := strings.EqualFold(strings.TrimSpace(h.env), "production")
	if isProduction {
		if _, hasQueryToken := c.GetQuery("token"); hasQueryToken {
			log.Warn().Msg("websocket handshake rejected: query token disabled in production")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "query parameter authentication is disabled in production"})
			return
		}
	}

	// 1. Authenticate user: prefer existing context user_id, then Authorization/Sec-WebSocket-Protocol (or ?token= outside production)
	userID := c.GetString("user_id")
	var selectedSubprotocol string

	if userID == "" {
		var tokenStr string
		tokenStr, selectedSubprotocol = extractToken(c, !isProduction)
		if tokenStr == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authentication token"})
			return
		}

		claims, err := h.tokenManager.ValidateAccessToken(c.Request.Context(), tokenStr)
		if err != nil {
			log.Warn().Err(err).Msg("websocket handshake authentication failed")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid authentication token"})
			return
		}
		if claims == nil || strings.TrimSpace(claims.UserID) == "" {
			log.Warn().Msg("websocket handshake authentication failed: token claims missing user id")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token claims: missing user id"})
			return
		}
		userID = claims.UserID
	}

	var responseHeader http.Header
	if selectedSubprotocol != "" {
		responseHeader = http.Header{
			"Sec-WebSocket-Protocol": []string{selectedSubprotocol},
		}
	}

	// 2. Upgrade HTTP connection to WebSocket
	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, responseHeader)
	if err != nil {
		log.Warn().Err(err).Msg("failed to upgrade websocket connection")
		return
	}

	// 3. Register client in hub (enforces per-user connection limits)
	client, err := h.hub.Register(userID, conn)
	if err != nil {
		log.Warn().Err(err).Str("user_id", userID).Msg("websocket connection rejected by hub")
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, err.Error()),
			time.Now().Add(time.Second),
		)
		_ = conn.Close()
		return
	}

	// 4. Start read and write pumps
	go client.WritePump()
	client.ReadPump() // blocks until socket closure
}

// extractToken pulls the JWT from Authorization: Bearer, Sec-WebSocket-Protocol,
// or (when allowQuery is true) query param ?token=.
func extractToken(c *gin.Context, allowQuery bool) (string, string) {
	if authHeader := c.GetHeader("Authorization"); authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if token := strings.TrimSpace(parts[1]); token != "" {
				return token, ""
			}
		}
	}

	if subprotocols := websocket.Subprotocols(c.Request); len(subprotocols) > 0 {
		for i := 0; i < len(subprotocols)-1; i++ {
			if strings.EqualFold(subprotocols[i], "bearer") || strings.EqualFold(subprotocols[i], "access_token") {
				if token := strings.TrimSpace(subprotocols[i+1]); token != "" {
					return token, subprotocols[i]
				}
			}
		}
		if len(subprotocols) == 1 {
			if token := strings.TrimSpace(subprotocols[0]); token != "" {
				return token, subprotocols[0]
			}
		}
	}

	if allowQuery {
		if token := strings.TrimSpace(c.Query("token")); token != "" {
			return token, ""
		}
	}

	return "", ""
}

// checkOrigin validates the Origin header against the configured allowed list.
// If allowedOrigins is empty, it falls back to same-origin verification.
func checkOrigin(r *http.Request, allowedOrigins []string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Non-browser clients (CLI, mobile native) often do not send Origin header
		return true
	}

	if len(allowedOrigins) == 0 {
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return strings.EqualFold(u.Host, r.Host)
	}

	for _, allowed := range allowedOrigins {
		if allowed == "*" || allowed == origin {
			return true
		}
	}

	return false
}
