package security

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// CheckURL validates a URL for SSRF safety.
// It blocks access to internal networks, metadata services, and non-HTTP protocols.
func CheckURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// Protocol check
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("blocked: only http/https protocols allowed, got %q", scheme)
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return fmt.Errorf("blocked: empty hostname")
	}

	// Block known metadata service hostnames
	lower := strings.ToLower(hostname)
	for _, blocked := range blockedHostnames {
		if lower == blocked || strings.HasSuffix(lower, "."+blocked) {
			return fmt.Errorf("blocked: metadata service hostname %q", hostname)
		}
	}

	// Resolve hostname and check IP ranges
	// DNS resolution failure should block, not silently pass (fail-closed)
	ips, err := net.LookupIP(hostname)
	if err != nil {
		return fmt.Errorf("DNS resolution failed for %q: %w", hostname, err)
	}

	for _, ip := range ips {
		if IsBlockedIP(ip) {
			return fmt.Errorf("blocked: %s resolves to internal IP %s", hostname, ip.String())
		}
	}

	return nil
}

var blockedHostnames = []string{
	"metadata.google.internal",
	"metadata.azure.com",
	"169.254.169.254",
}

// IsBlockedIP checks if an IP address is in a blocked range.
// Exported for use by HTTP transports that need to validate IPs at connection time.
func IsBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() {
		return true
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip.IsPrivate() {
		return true
	}
	if ip.IsUnspecified() {
		return true
	}

	// Check specific ranges that net.IP methods might miss
	for _, cidr := range blockedCIDRs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(ip) {
			return true
		}
	}

	return false
}

// blockedCIDRs are additional IP ranges to block beyond what net.IP catches.
var blockedCIDRs = []string{
	"169.254.0.0/16", // Link-local (AWS/GCP metadata)
	"100.64.0.0/10",  // Carrier-grade NAT
	"198.18.0.0/15",  // Benchmarking
	"192.0.0.0/24",   // IETF protocol assignments
	"192.0.2.0/24",   // TEST-NET-1
	"198.51.100.0/24", // TEST-NET-2
	"203.0.113.0/24",  // TEST-NET-3
}
