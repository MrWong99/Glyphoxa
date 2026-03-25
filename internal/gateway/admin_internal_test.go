package gateway

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		xff        string
		remoteAddr string
		want       string
	}{
		{
			name:       "X-Forwarded-For single IP",
			xff:        "10.0.0.1",
			remoteAddr: "192.168.1.1:1234",
			want:       "10.0.0.1",
		},
		{
			name:       "X-Forwarded-For chain",
			xff:        "10.0.0.1, 172.16.0.1, 192.168.0.1",
			remoteAddr: "192.168.1.1:1234",
			want:       "10.0.0.1",
		},
		{
			name:       "X-Forwarded-For with whitespace",
			xff:        "  10.0.0.2 , proxy",
			remoteAddr: "192.168.1.1:1234",
			want:       "10.0.0.2",
		},
		{
			name:       "no XFF uses RemoteAddr host",
			xff:        "",
			remoteAddr: "192.168.1.1:1234",
			want:       "192.168.1.1",
		},
		{
			name:       "no XFF and no port in RemoteAddr",
			xff:        "",
			remoteAddr: "192.168.1.1",
			want:       "192.168.1.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}

			got := clientIP(req)
			if got != tt.want {
				t.Errorf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
