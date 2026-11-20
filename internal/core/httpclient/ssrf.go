package httpclient

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	blockedCIDRStrings = []string{
		"0.0.0.0/8",          // Current network ("this" network)
		"10.0.0.0/8",         // RFC 1918 Private
		"100.64.0.0/10",      // Shared Address Space / CGNAT
		"127.0.0.0/8",        // Loopback
		"169.254.0.0/16",     // Link-Local / AWS IMDS
		"172.16.0.0/12",      // RFC 1918 Private
		"192.0.0.0/24",       // IETF Protocol Assignments
		"192.0.2.0/24",       // TEST-NET-1 Documentation
		"192.168.0.0/16",     // RFC 1918 Private
		"198.18.0.0/15",      // Network benchmark tests
		"198.51.100.0/24",    // TEST-NET-2 Documentation
		"203.0.113.0/24",     // TEST-NET-3 Documentation
		"224.0.0.0/4",        // Multicast
		"240.0.0.0/4",        // Reserved / Future Use
		"255.255.255.255/32", // Limited Broadcast
		"::/128",             // IPv6 Unspecified
		"::1/128",            // IPv6 Loopback
		"fc00::/7",           // IPv6 Unique Local Address (ULA)
		"fe80::/10",          // IPv6 Link-Local Unicast
		"2001:db8::/32",      // IPv6 Documentation
		"ff00::/8",           // IPv6 Multicast
	}

	blockedIPNets []*net.IPNet
	initCIDRsOnce sync.Once
)

func init() {
	initBlockedCIDRs()
}

func initBlockedCIDRs() {
	initCIDRsOnce.Do(func() {
		for _, cidr := range blockedCIDRStrings {
			_, ipNet, err := net.ParseCIDR(cidr)
			if err == nil && ipNet != nil {
				blockedIPNets = append(blockedIPNets, ipNet)
			}
		}
	})
}

// IsBlockedIP returns true if the specified IP address is in a private, loopback,
// link-local, multicast, or reserved CIDR range.
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}

	// Standard library checks
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}

	// Explicit CIDR range checks (covers CGNAT, AWS IMDS, documentation IPs, etc.)
	for _, ipNet := range blockedIPNets {
		if ipNet.Contains(ip) {
			return true
		}
	}

	return false
}

// AllowPrivateURLs returns whether private and local URLs are permitted,
// controlled via the HTTP_CLIENT_ALLOW_PRIVATE_URLS environment variable.
func AllowPrivateURLs() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("HTTP_CLIENT_ALLOW_PRIVATE_URLS")))
	return v == "true" || v == "1" || v == "yes"
}

// NewSafeDialContext returns a DialContext function equipped with DNS validation
// and raw socket Control checks that validates all resolved IPs against SSRF rules.
func NewSafeDialContext(allowPrivate bool, timeout, keepAlive time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if keepAlive <= 0 {
		keepAlive = 30 * time.Second
	}

	baseDialer := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: keepAlive,
		Control: func(network, address string, c syscall.RawConn) error {
			if allowPrivate || AllowPrivateURLs() {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				host = address
			}
			if ip := net.ParseIP(host); ip != nil {
				if IsBlockedIP(ip) {
					return fmt.Errorf("blocked private IP: %s is not allowed", ip.String())
				}
			}
			return nil
		},
	}

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if allowPrivate || AllowPrivateURLs() {
			return baseDialer.DialContext(ctx, network, addr)
		}

		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q: %w", addr, err)
		}

		// Direct IP address check
		if ip := net.ParseIP(host); ip != nil {
			if IsBlockedIP(ip) {
				return nil, fmt.Errorf("blocked private IP: %s is not allowed", ip.String())
			}
			return baseDialer.DialContext(ctx, network, addr)
		}

		// Hostname resolution and validation
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no IP addresses resolved for host %q", host)
		}

		// Validate all resolved IP addresses against blocked ranges
		for _, ipAddr := range ips {
			if IsBlockedIP(ipAddr.IP) {
				return nil, fmt.Errorf("blocked private IP: resolved address %s for host %q is not allowed", ipAddr.IP.String(), host)
			}
		}

		// Pin dial to the first validated IP to prevent DNS rebinding TOCTOU
		safeAddr := net.JoinHostPort(ips[0].IP.String(), port)
		return baseDialer.DialContext(ctx, network, safeAddr)
	}
}
