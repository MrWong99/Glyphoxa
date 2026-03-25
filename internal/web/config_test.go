package web

import (
	"os"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
		check   func(t *testing.T, cfg *Config)
	}{
		{
			name: "valid discord config",
			env: map[string]string{
				"GLYPHOXA_WEB_DATABASE_DSN":          "postgres://localhost/test",
				"GLYPHOXA_WEB_JWT_SECRET":            "a-very-long-jwt-secret-that-is-at-least-32-chars",
				"GLYPHOXA_WEB_DISCORD_CLIENT_ID":     "id",
				"GLYPHOXA_WEB_DISCORD_CLIENT_SECRET": "secret",
				"GLYPHOXA_WEB_DISCORD_REDIRECT_URI":  "http://localhost/callback",
				"GLYPHOXA_WEB_LISTEN_ADDR":           ":9090",
				"GLYPHOXA_WEB_ALLOWED_ORIGINS":       "http://localhost:3000, https://app.example.com",
				"GLYPHOXA_WEB_GATEWAY_GRPC_ADDR":     "gateway:50051",
				"GLYPHOXA_WEB_GATEWAY_SECRET":        "shared-secret",
			},
			wantErr: false,
			check: func(t *testing.T, cfg *Config) {
				t.Helper()
				if cfg.ListenAddr != ":9090" {
					t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":9090")
				}
				if len(cfg.AllowedOrigins) != 2 {
					t.Errorf("AllowedOrigins = %d, want 2", len(cfg.AllowedOrigins))
				}
				if cfg.GatewayGRPCAddr != "gateway:50051" {
					t.Errorf("GatewayGRPCAddr = %q", cfg.GatewayGRPCAddr)
				}
				if cfg.GatewaySharedSecret != "shared-secret" {
					t.Errorf("GatewaySharedSecret = %q", cfg.GatewaySharedSecret)
				}
			},
		},
		{
			name: "valid apikey config",
			env: map[string]string{
				"GLYPHOXA_WEB_DATABASE_DSN": "postgres://localhost/test",
				"GLYPHOXA_WEB_JWT_SECRET":   "a-very-long-jwt-secret-that-is-at-least-32-chars",
				"GLYPHOXA_WEB_ADMIN_KEY":    "admin-key",
			},
			wantErr: false,
			check: func(t *testing.T, cfg *Config) {
				t.Helper()
				if cfg.AdminAPIKey != "admin-key" {
					t.Errorf("AdminAPIKey = %q, want %q", cfg.AdminAPIKey, "admin-key")
				}
				// Default listen addr.
				if cfg.ListenAddr != ":8090" {
					t.Errorf("ListenAddr = %q, want %q (default)", cfg.ListenAddr, ":8090")
				}
			},
		},
		{
			name: "fallback to GLYPHOXA_ADMIN_API_KEY",
			env: map[string]string{
				"GLYPHOXA_WEB_DATABASE_DSN": "postgres://localhost/test",
				"GLYPHOXA_WEB_JWT_SECRET":   "a-very-long-jwt-secret-that-is-at-least-32-chars",
				"GLYPHOXA_ADMIN_API_KEY":    "fallback-key",
			},
			wantErr: false,
			check: func(t *testing.T, cfg *Config) {
				t.Helper()
				if cfg.AdminAPIKey != "fallback-key" {
					t.Errorf("AdminAPIKey = %q, want %q (from fallback env)", cfg.AdminAPIKey, "fallback-key")
				}
			},
		},
		{
			name:    "empty env fails validation",
			env:     map[string]string{},
			wantErr: true,
		},
		{
			name: "origins with empty entries trimmed",
			env: map[string]string{
				"GLYPHOXA_WEB_DATABASE_DSN":    "postgres://localhost/test",
				"GLYPHOXA_WEB_JWT_SECRET":      "a-very-long-jwt-secret-that-is-at-least-32-chars",
				"GLYPHOXA_WEB_ADMIN_KEY":       "key",
				"GLYPHOXA_WEB_ALLOWED_ORIGINS": "http://a.com, , http://b.com, ",
			},
			wantErr: false,
			check: func(t *testing.T, cfg *Config) {
				t.Helper()
				if len(cfg.AllowedOrigins) != 2 {
					t.Errorf("AllowedOrigins = %v, want 2 entries (empty trimmed)", cfg.AllowedOrigins)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel — modifies os.Environ.

			// Save and clear all GLYPHOXA_ env vars.
			envKeys := []string{
				"GLYPHOXA_WEB_DATABASE_DSN",
				"GLYPHOXA_WEB_JWT_SECRET",
				"GLYPHOXA_WEB_DISCORD_CLIENT_ID",
				"GLYPHOXA_WEB_DISCORD_CLIENT_SECRET",
				"GLYPHOXA_WEB_DISCORD_REDIRECT_URI",
				"GLYPHOXA_WEB_ADMIN_KEY",
				"GLYPHOXA_ADMIN_API_KEY",
				"GLYPHOXA_WEB_GATEWAY_URL",
				"GLYPHOXA_WEB_GATEWAY_GRPC_ADDR",
				"GLYPHOXA_WEB_GATEWAY_TLS_CERT",
				"GLYPHOXA_WEB_GATEWAY_TLS_KEY",
				"GLYPHOXA_WEB_GATEWAY_TLS_CA",
				"GLYPHOXA_WEB_GATEWAY_SECRET",
				"GLYPHOXA_WEB_ALLOWED_ORIGINS",
				"GLYPHOXA_WEB_LISTEN_ADDR",
			}

			saved := make(map[string]string)
			for _, k := range envKeys {
				if v, ok := os.LookupEnv(k); ok {
					saved[k] = v
				}
				os.Unsetenv(k)
			}
			t.Cleanup(func() {
				for _, k := range envKeys {
					os.Unsetenv(k)
				}
				for k, v := range saved {
					os.Setenv(k, v)
				}
			})

			for k, v := range tt.env {
				os.Setenv(k, v)
			}

			cfg, err := LoadConfig()
			if (err != nil) != tt.wantErr {
				t.Errorf("LoadConfig() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if tt.check != nil && cfg != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestConfigValidate_mTLS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid with all mTLS fields",
			cfg: Config{
				DatabaseDSN:    "postgres://localhost/test",
				JWTSecret:      "a-very-long-jwt-secret-that-is-at-least-32-chars",
				AdminAPIKey:    "key",
				GatewayTLSCert: "/path/to/cert.pem",
				GatewayTLSKey:  "/path/to/key.pem",
				GatewayTLSCA:   "/path/to/ca.pem",
			},
			wantErr: false,
		},
		{
			name: "partial mTLS - only cert",
			cfg: Config{
				DatabaseDSN:    "postgres://localhost/test",
				JWTSecret:      "a-very-long-jwt-secret-that-is-at-least-32-chars",
				AdminAPIKey:    "key",
				GatewayTLSCert: "/path/to/cert.pem",
			},
			wantErr: true,
		},
		{
			name: "partial mTLS - cert and key but no CA",
			cfg: Config{
				DatabaseDSN:    "postgres://localhost/test",
				JWTSecret:      "a-very-long-jwt-secret-that-is-at-least-32-chars",
				AdminAPIKey:    "key",
				GatewayTLSCert: "/path/to/cert.pem",
				GatewayTLSKey:  "/path/to/key.pem",
			},
			wantErr: true,
		},
		{
			name: "no mTLS fields is fine",
			cfg: Config{
				DatabaseDSN: "postgres://localhost/test",
				JWTSecret:   "a-very-long-jwt-secret-that-is-at-least-32-chars",
				AdminAPIKey: "key",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}
