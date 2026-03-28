package web

import (
	"net"
	"testing"
)

func TestValidateBaseURL(t *testing.T) {
	t.Parallel()

	// mockResolver returns a resolver function that maps hostnames to IPs.
	mockResolver := func(mapping map[string][]net.IP) func(string) ([]net.IP, error) {
		return func(host string) ([]net.IP, error) {
			ips, ok := mapping[host]
			if !ok {
				return nil, &net.DNSError{Err: "no such host", Name: host}
			}
			return ips, nil
		}
	}

	tests := []struct {
		name     string
		url      string
		resolver func(string) ([]net.IP, error)
		wantErr  bool
	}{
		// -- Allowed cases --
		{
			name:     "empty URL is allowed",
			url:      "",
			resolver: nil,
			wantErr:  false,
		},
		{
			name: "public HTTPS URL",
			url:  "https://api.openai.com",
			resolver: mockResolver(map[string][]net.IP{
				"api.openai.com": {net.ParseIP("104.18.7.192")},
			}),
			wantErr: false,
		},
		{
			name: "public HTTP URL",
			url:  "http://api.example.com",
			resolver: mockResolver(map[string][]net.IP{
				"api.example.com": {net.ParseIP("93.184.216.34")},
			}),
			wantErr: false,
		},
		{
			name:     "public IP literal",
			url:      "https://93.184.216.34",
			resolver: nil,
			wantErr:  false,
		},
		{
			name: "URL with port",
			url:  "https://api.example.com:8443",
			resolver: mockResolver(map[string][]net.IP{
				"api.example.com": {net.ParseIP("93.184.216.34")},
			}),
			wantErr: false,
		},
		{
			name: "URL with path",
			url:  "https://api.example.com/v1",
			resolver: mockResolver(map[string][]net.IP{
				"api.example.com": {net.ParseIP("93.184.216.34")},
			}),
			wantErr: false,
		},

		// -- Blocked: Private IPv4 ranges (RFC 1918) --
		{
			name:     "10.0.0.0/8 IP literal",
			url:      "https://10.0.0.1",
			resolver: nil,
			wantErr:  true,
		},
		{
			name:     "172.16.0.0/12 IP literal",
			url:      "https://172.16.0.1",
			resolver: nil,
			wantErr:  true,
		},
		{
			name:     "172.31.x.x IP literal",
			url:      "https://172.31.255.255",
			resolver: nil,
			wantErr:  true,
		},
		{
			name:     "192.168.0.0/16 IP literal",
			url:      "https://192.168.1.1",
			resolver: nil,
			wantErr:  true,
		},

		// -- Blocked: Loopback --
		{
			name:     "127.0.0.1 IP literal",
			url:      "https://127.0.0.1",
			resolver: nil,
			wantErr:  true,
		},
		{
			name:     "127.0.0.2 IP literal",
			url:      "https://127.0.0.2",
			resolver: nil,
			wantErr:  true,
		},
		{
			name:     "IPv6 loopback",
			url:      "https://[::1]",
			resolver: nil,
			wantErr:  true,
		},

		// -- Blocked: Link-local --
		{
			name:     "link-local 169.254.x.x",
			url:      "https://169.254.1.1",
			resolver: nil,
			wantErr:  true,
		},

		// -- Blocked: Cloud metadata --
		{
			name:     "AWS/GCP metadata IP",
			url:      "http://169.254.169.254",
			resolver: nil,
			wantErr:  true,
		},
		{
			name: "metadata.google.internal hostname",
			url:  "http://metadata.google.internal",
			resolver: mockResolver(map[string][]net.IP{
				"metadata.google.internal": {net.ParseIP("169.254.169.254")},
			}),
			wantErr: true,
		},

		// -- Blocked: Unspecified --
		{
			name:     "0.0.0.0",
			url:      "https://0.0.0.0",
			resolver: nil,
			wantErr:  true,
		},
		{
			name:     "IPv6 unspecified",
			url:      "https://[::]",
			resolver: nil,
			wantErr:  true,
		},

		// -- Blocked: Carrier-grade NAT (RFC 6598) --
		{
			name:     "100.64.0.0/10",
			url:      "https://100.64.0.1",
			resolver: nil,
			wantErr:  true,
		},
		{
			name:     "100.127.255.255",
			url:      "https://100.127.255.255",
			resolver: nil,
			wantErr:  true,
		},

		// -- Blocked: Exact hostnames --
		{
			name: "localhost",
			url:  "https://localhost",
			resolver: mockResolver(map[string][]net.IP{
				"localhost": {net.ParseIP("127.0.0.1")},
			}),
			wantErr: true,
		},
		{
			name: "LOCALHOST (case insensitive)",
			url:  "https://LOCALHOST",
			resolver: mockResolver(map[string][]net.IP{
				"localhost": {net.ParseIP("127.0.0.1")},
			}),
			wantErr: true,
		},

		// -- Blocked: DNS suffixes --
		{
			name: ".internal suffix",
			url:  "https://service.internal",
			resolver: mockResolver(map[string][]net.IP{
				"service.internal": {net.ParseIP("10.0.0.5")},
			}),
			wantErr: true,
		},
		{
			name: ".local suffix",
			url:  "https://myservice.local",
			resolver: mockResolver(map[string][]net.IP{
				"myservice.local": {net.ParseIP("192.168.1.100")},
			}),
			wantErr: true,
		},
		{
			name: ".localhost suffix",
			url:  "https://evil.localhost",
			resolver: mockResolver(map[string][]net.IP{
				"evil.localhost": {net.ParseIP("127.0.0.1")},
			}),
			wantErr: true,
		},
		{
			name: "Kubernetes service DNS",
			url:  "https://my-svc.default.svc.cluster.local",
			resolver: mockResolver(map[string][]net.IP{
				"my-svc.default.svc.cluster.local": {net.ParseIP("10.96.0.1")},
			}),
			wantErr: true,
		},
		{
			name: "Kubernetes pod DNS",
			url:  "https://10-244-0-5.default.pod.cluster.local",
			resolver: mockResolver(map[string][]net.IP{
				"10-244-0-5.default.pod.cluster.local": {net.ParseIP("10.244.0.5")},
			}),
			wantErr: true,
		},
		{
			name: "short Kubernetes .svc suffix",
			url:  "https://my-service.default.svc",
			resolver: mockResolver(map[string][]net.IP{
				"my-service.default.svc": {net.ParseIP("10.96.0.2")},
			}),
			wantErr: true,
		},

		// -- Blocked: DNS resolves to private IP (TOCTOU-aware) --
		{
			name: "public hostname resolving to private IP",
			url:  "https://evil.example.com",
			resolver: mockResolver(map[string][]net.IP{
				"evil.example.com": {net.ParseIP("10.0.0.1")},
			}),
			wantErr: true,
		},
		{
			name: "hostname resolving to loopback",
			url:  "https://sneaky.example.com",
			resolver: mockResolver(map[string][]net.IP{
				"sneaky.example.com": {net.ParseIP("127.0.0.1")},
			}),
			wantErr: true,
		},
		{
			name: "hostname resolving to metadata IP",
			url:  "https://metadata-proxy.example.com",
			resolver: mockResolver(map[string][]net.IP{
				"metadata-proxy.example.com": {net.ParseIP("169.254.169.254")},
			}),
			wantErr: true,
		},
		{
			name: "one of multiple IPs is private",
			url:  "https://dual.example.com",
			resolver: mockResolver(map[string][]net.IP{
				"dual.example.com": {
					net.ParseIP("93.184.216.34"),
					net.ParseIP("10.0.0.5"),
				},
			}),
			wantErr: true,
		},

		// -- Blocked: Invalid URLs --
		{
			name:     "non-http scheme (ftp)",
			url:      "ftp://files.example.com",
			resolver: nil,
			wantErr:  true,
		},
		{
			name:     "non-http scheme (file)",
			url:      "file:///etc/passwd",
			resolver: nil,
			wantErr:  true,
		},
		{
			name:     "no hostname",
			url:      "https://",
			resolver: nil,
			wantErr:  true,
		},

		// -- Blocked: DNS resolution fails --
		{
			name:     "unresolvable hostname",
			url:      "https://nonexistent.example.invalid",
			resolver: mockResolver(map[string][]net.IP{
				// not in mapping
			}),
			wantErr: true,
		},

		// -- Blocked: metadata exact hostname --
		{
			name: "metadata shortname",
			url:  "http://metadata",
			resolver: mockResolver(map[string][]net.IP{
				"metadata": {net.ParseIP("169.254.169.254")},
			}),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateBaseURL(tt.url, tt.resolver)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateBaseURL(%q) error = %v, wantErr = %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestIsPrivateIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"loopback v4", "127.0.0.1", true},
		{"loopback v6", "::1", true},
		{"private 10.x", "10.0.0.1", true},
		{"private 172.16.x", "172.16.0.1", true},
		{"private 192.168.x", "192.168.0.1", true},
		{"link-local v4", "169.254.1.1", true},
		{"link-local v6", "fe80::1", true},
		{"unspecified v4", "0.0.0.0", true},
		{"unspecified v6", "::", true},
		{"public v4", "8.8.8.8", false},
		{"public v6", "2001:4860:4860::8888", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("failed to parse IP %q", tt.ip)
			}
			got := isPrivateIP(ip)
			if got != tt.want {
				t.Errorf("isPrivateIP(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestIsBlockedCIDR(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"metadata IP", "169.254.169.254", true},
		{"CGNAT start", "100.64.0.1", true},
		{"CGNAT end", "100.127.255.255", true},
		{"this network 0.x", "0.0.0.1", true},
		{"just above CGNAT", "100.128.0.0", false},
		{"public", "8.8.8.8", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("failed to parse IP %q", tt.ip)
			}
			got := isBlockedCIDR(ip)
			if got != tt.want {
				t.Errorf("isBlockedCIDR(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}
