package security

import (
	"errors"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/oschwald/geoip2-golang"

	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/platform/logger"
)

// GeoIPReader represents the minimal contract required to lookup country records from GeoIP DB.
type GeoIPReader interface {
	Country(ipAddress net.IP) (*geoip2.Country, error)
	Close() error
}

// GeoIPService provides IP-to-country verification and country allowlist enforcement.
type GeoIPService struct {
	reader           GeoIPReader
	allowedCountries map[string]struct{}
}

// NewGeoIPService loads a MaxMind GeoLite2 country database and configures allowed ISO country codes.
// If the DB file does not exist, is inaccessible, or is empty, it fails open gracefully with a warning.
func NewGeoIPService(dbPath string, allowedCountries []string) (*GeoIPService, error) {
	allowedMap := make(map[string]struct{}, len(allowedCountries))
	for _, c := range allowedCountries {
		c = strings.TrimSpace(strings.ToUpper(c))
		if c != "" {
			allowedMap[c] = struct{}{}
		}
	}

	if strings.TrimSpace(dbPath) == "" {
		logger.Default().Warn().Msg("GeoIP DB path is empty; GeoIPService running in fail-open mode")
		return &GeoIPService{reader: nil, allowedCountries: allowedMap}, nil
	}

	stat, err := os.Stat(dbPath)
	if err != nil {
		logger.Default().Warn().Err(err).Str("path", dbPath).Msg("GeoIP DB file not found or inaccessible; GeoIPService running in fail-open mode")
		return &GeoIPService{reader: nil, allowedCountries: allowedMap}, nil
	}
	if stat.Size() == 0 {
		logger.Default().Warn().Str("path", dbPath).Msg("GeoIP DB file is empty; GeoIPService running in fail-open mode")
		return &GeoIPService{reader: nil, allowedCountries: allowedMap}, nil
	}

	reader, err := geoip2.Open(dbPath)
	if err != nil {
		logger.Default().Warn().Err(err).Str("path", dbPath).Msg("failed to open GeoIP DB; GeoIPService running in fail-open mode")
		return &GeoIPService{reader: nil, allowedCountries: allowedMap}, nil
	}

	return &GeoIPService{reader: reader, allowedCountries: allowedMap}, nil
}

// NewGeoIPServiceWithReader constructs a GeoIPService using an injected GeoIPReader.
func NewGeoIPServiceWithReader(reader GeoIPReader, allowedCountries []string) *GeoIPService {
	allowedMap := make(map[string]struct{}, len(allowedCountries))
	for _, c := range allowedCountries {
		c = strings.TrimSpace(strings.ToUpper(c))
		if c != "" {
			allowedMap[c] = struct{}{}
		}
	}
	return &GeoIPService{reader: reader, allowedCountries: allowedMap}
}

// NewGeoIPServiceFromConfig constructs a GeoIPService from application SecurityConfig.
func NewGeoIPServiceFromConfig(cfg config.SecurityConfig) (*GeoIPService, error) {
	return NewGeoIPService(cfg.GeoIPDBPath, cfg.GeoIPAllowedCountries)
}

// Close closes the underlying GeoIP database reader if present.
func (s *GeoIPService) Close() error {
	if s.reader != nil {
		return s.reader.Close()
	}
	return nil
}

// IsAllowed evaluates whether the specified IP string is permitted based on private exemptions and country allowlists.
func (s *GeoIPService) IsAllowed(ipStr string) (bool, error) {
	ip := ParseIP(ipStr)
	return s.IsAllowedIP(ip)
}

// IsAllowedIP evaluates whether the net.IP is permitted based on private exemptions and country allowlists.
func (s *GeoIPService) IsAllowedIP(ip net.IP) (bool, error) {
	if ip == nil {
		return false, errors.New("invalid IP address")
	}

	// Always exempt private and loopback networks
	if IsPrivateOrLoopback(ip) {
		return true, nil
	}

	// If no country restrictions configured, permit all
	if len(s.allowedCountries) == 0 {
		return true, nil
	}

	// Fail-open if reader is nil (e.g. database not found/configured)
	if s.reader == nil {
		return true, nil
	}

	record, err := s.reader.Country(ip)
	if err != nil {
		return false, err
	}

	iso := strings.ToUpper(strings.TrimSpace(record.Country.IsoCode))
	if iso == "" {
		iso = strings.ToUpper(strings.TrimSpace(record.RegisteredCountry.IsoCode))
	}

	if _, allowed := s.allowedCountries[iso]; allowed {
		return true, nil
	}

	return false, nil
}

// ParseIP strips optional port and returns net.IP.
func ParseIP(ipStr string) net.IP {
	ipStr = strings.TrimSpace(ipStr)
	if host, _, err := net.SplitHostPort(ipStr); err == nil {
		ipStr = host
	}
	return net.ParseIP(ipStr)
}

// IsPrivateOrLoopback returns true if ip belongs to RFC 1918, RFC 4193, loopback, or link-local address spaces.
func IsPrivateOrLoopback(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4.IsLoopback() || ip4.IsPrivate() || ip4.IsLinkLocalUnicast() || ip4.IsUnspecified()
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// GeoIPFilter returns a Gin middleware enforcing GeoIP country allowlists.
func GeoIPFilter(svc *GeoIPService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil {
			c.Next()
			return
		}

		clientIP := c.ClientIP()
		allowed, err := svc.IsAllowed(clientIP)
		if err != nil || !allowed {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "Forbidden - country not allowed",
			})
			return
		}

		c.Next()
	}
}
