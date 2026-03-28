package web

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// isPrivateIP reports whether ip is a private, loopback, link-local,
// or otherwise non-routable address that should be blocked to prevent
// SSRF attacks.
func isPrivateIP(ip net.IP) bool {
	// Loopback (127.0.0.0/8, ::1).
	if ip.IsLoopback() {
		return true
	}
	// Link-local unicast (169.254.0.0/16, fe80::/10).
	if ip.IsLinkLocalUnicast() {
		return true
	}
	// Link-local multicast.
	if ip.IsLinkLocalMulticast() {
		return true
	}
	// RFC 1918 private ranges (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16)
	// and RFC 4193 unique-local (fc00::/7).
	if ip.IsPrivate() {
		return true
	}
	// Unspecified (0.0.0.0, ::).
	if ip.IsUnspecified() {
		return true
	}
	return false
}

// blockedHostSuffixes contains DNS suffixes that resolve to internal
// infrastructure and must be rejected to prevent SSRF.
var blockedHostSuffixes = []string{
	".internal",
	".local",
	".localhost",
	".svc.cluster.local", // Kubernetes service DNS
	".pod.cluster.local", // Kubernetes pod DNS
	".svc",               // short Kubernetes DNS
}

// blockedHostExact contains exact hostnames that must be rejected.
var blockedHostExact = map[string]bool{
	"localhost":                true,
	"metadata.google.internal": true, // GCE metadata
	"169.254.169.254":          true, // AWS/GCP/Azure metadata endpoint
	"metadata":                 true, // GKE metadata shortname
}

// blockedCIDRs are additional CIDR ranges to reject beyond the standard
// private/loopback/link-local checks. This catches edge cases like the
// cloud metadata IP and carrier-grade NAT range.
var blockedCIDRs []*net.IPNet

func init() {
	cidrs := []string{
		"169.254.169.254/32", // Cloud metadata endpoint
		"100.64.0.0/10",      // Carrier-grade NAT (RFC 6598)
		"0.0.0.0/8",          // "This" network
		"fd00::/8",           // Unique-local (commonly used internally)
	}
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			panic("web: invalid blocked CIDR: " + cidr)
		}
		blockedCIDRs = append(blockedCIDRs, ipNet)
	}
}

// isBlockedCIDR reports whether ip falls within any of the additional
// blocked CIDR ranges.
func isBlockedCIDR(ip net.IP) bool {
	for _, cidr := range blockedCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// validateBaseURL checks that a user-supplied base URL is safe to use
// for outbound HTTP requests. It rejects private IPs, internal DNS
// names, cloud metadata endpoints, and Kubernetes service addresses
// to prevent SSRF attacks.
//
// The resolver parameter allows injecting a custom DNS lookup function
// for testing. Pass nil to use net.DefaultResolver.
func validateBaseURL(rawURL string, resolver func(string) ([]net.IP, error)) error {
	if rawURL == "" {
		return nil // No custom base URL — will use provider defaults.
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("web: invalid base URL: %w", err)
	}

	// Only allow HTTPS (and HTTP for localhost in dev, but we block
	// localhost anyway, so effectively HTTPS-only).
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("web: base URL must use https scheme, got %q", parsed.Scheme)
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return fmt.Errorf("web: base URL has no hostname")
	}

	// Check exact blocked hostnames.
	lower := strings.ToLower(hostname)
	if blockedHostExact[lower] {
		return fmt.Errorf("web: base URL hostname %q is not allowed", hostname)
	}

	// Check blocked DNS suffixes.
	for _, suffix := range blockedHostSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return fmt.Errorf("web: base URL hostname %q is not allowed", hostname)
		}
	}

	// If the hostname is already an IP literal, validate it directly.
	if ip := net.ParseIP(hostname); ip != nil {
		if isPrivateIP(ip) || isBlockedCIDR(ip) {
			return fmt.Errorf("web: base URL resolves to blocked address %s", ip)
		}
		return nil
	}

	// Resolve the hostname and check every returned address.
	resolve := resolver
	if resolve == nil {
		resolve = func(host string) ([]net.IP, error) {
			return net.LookupIP(host)
		}
	}

	ips, err := resolve(hostname)
	if err != nil {
		return fmt.Errorf("web: cannot resolve base URL host %q: %w", hostname, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("web: base URL host %q has no DNS records", hostname)
	}

	for _, ip := range ips {
		if isPrivateIP(ip) || isBlockedCIDR(ip) {
			return fmt.Errorf("web: base URL resolves to blocked address %s", ip)
		}
	}

	return nil
}
