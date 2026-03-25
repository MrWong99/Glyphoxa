// Package web implements the Glyphoxa web management service.
//
// The service provides self-service management for Dungeon Masters: user
// authentication via Discord OAuth2, campaign and NPC CRUD, session and usage
// queries, and tenant administration. It runs as a standalone binary alongside
// the Glyphoxa gateway and shares the same PostgreSQL database.
package web

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Config holds all configuration for the web management service.
// Values are loaded from environment variables.
type Config struct {
	// DatabaseDSN is the PostgreSQL connection string.
	DatabaseDSN string

	// JWTSecret is the HMAC key used to sign and verify JWTs.
	JWTSecret string

	// DiscordClientID is the Discord OAuth2 application client ID.
	// Optional when AdminAPIKey is set.
	DiscordClientID string

	// DiscordClientSecret is the Discord OAuth2 application client secret.
	// Optional when AdminAPIKey is set.
	DiscordClientSecret string

	// DiscordRedirectURI is the OAuth2 callback URL registered with Discord.
	// Optional when AdminAPIKey is set.
	DiscordRedirectURI string

	// GoogleClientID is the Google OAuth2 application client ID.
	GoogleClientID string

	// GoogleClientSecret is the Google OAuth2 application client secret.
	GoogleClientSecret string

	// GoogleRedirectURI is the OAuth2 callback URL registered with Google.
	GoogleRedirectURI string

	// GitHubClientID is the GitHub OAuth2 application client ID.
	GitHubClientID string

	// GitHubClientSecret is the GitHub OAuth2 application client secret.
	GitHubClientSecret string

	// GitHubRedirectURI is the OAuth2 callback URL registered with GitHub.
	GitHubRedirectURI string

	// AdminAPIKey is the shared admin key that can be used as a login
	// fallback when Discord OAuth2 is not configured. Loaded from
	// GLYPHOXA_WEB_ADMIN_KEY or GLYPHOXA_ADMIN_API_KEY.
	AdminAPIKey string

	// GatewayURL is the base URL of the gateway's internal admin API.
	// Deprecated: use GatewayGRPCAddr instead.
	GatewayURL string

	// GatewayGRPCAddr is the address of the gateway's gRPC server
	// (e.g. "gateway:50051"). Used for tenant CRUD, session control,
	// usage queries, and bot status via the ManagementService.
	GatewayGRPCAddr string

	// GatewayTLSCert is the path to the client TLS certificate for mTLS
	// to the gateway gRPC server.
	GatewayTLSCert string

	// GatewayTLSKey is the path to the client TLS private key for mTLS.
	GatewayTLSKey string

	// GatewayTLSCA is the path to the CA certificate used to verify the
	// gateway's server certificate.
	GatewayTLSCA string

	// GatewaySharedSecret is the shared secret for authenticating to the
	// gateway's ManagementService gRPC endpoint. When set, the secret is
	// sent in gRPC metadata on every management RPC.
	GatewaySharedSecret string

	// AllowedOrigins is the list of origins permitted by CORS. An empty list
	// defaults to ["*"] (allow all), which is acceptable for development but
	// should be restricted in production.
	AllowedOrigins []string

	// ListenAddr is the address the HTTP server binds to.
	ListenAddr string
}

// LoadConfig reads configuration from environment variables and validates
// that all required values are present.
func LoadConfig() (*Config, error) {
	// AdminAPIKey: prefer GLYPHOXA_WEB_ADMIN_KEY, fall back to GLYPHOXA_ADMIN_API_KEY.
	adminKey := os.Getenv("GLYPHOXA_WEB_ADMIN_KEY")
	if adminKey == "" {
		adminKey = os.Getenv("GLYPHOXA_ADMIN_API_KEY")
	}

	cfg := &Config{
		DatabaseDSN:         os.Getenv("GLYPHOXA_WEB_DATABASE_DSN"),
		JWTSecret:           os.Getenv("GLYPHOXA_WEB_JWT_SECRET"),
		DiscordClientID:     os.Getenv("GLYPHOXA_WEB_DISCORD_CLIENT_ID"),
		DiscordClientSecret: os.Getenv("GLYPHOXA_WEB_DISCORD_CLIENT_SECRET"),
		DiscordRedirectURI:  os.Getenv("GLYPHOXA_WEB_DISCORD_REDIRECT_URI"),
		GoogleClientID:      os.Getenv("GLYPHOXA_WEB_GOOGLE_CLIENT_ID"),
		GoogleClientSecret:  os.Getenv("GLYPHOXA_WEB_GOOGLE_CLIENT_SECRET"),
		GoogleRedirectURI:   os.Getenv("GLYPHOXA_WEB_GOOGLE_REDIRECT_URI"),
		GitHubClientID:      os.Getenv("GLYPHOXA_WEB_GITHUB_CLIENT_ID"),
		GitHubClientSecret:  os.Getenv("GLYPHOXA_WEB_GITHUB_CLIENT_SECRET"),
		GitHubRedirectURI:   os.Getenv("GLYPHOXA_WEB_GITHUB_REDIRECT_URI"),
		AdminAPIKey:         adminKey,
		GatewayURL:          os.Getenv("GLYPHOXA_WEB_GATEWAY_URL"),
		GatewayGRPCAddr:     os.Getenv("GLYPHOXA_WEB_GATEWAY_GRPC_ADDR"),
		GatewayTLSCert:      os.Getenv("GLYPHOXA_WEB_GATEWAY_TLS_CERT"),
		GatewayTLSKey:       os.Getenv("GLYPHOXA_WEB_GATEWAY_TLS_KEY"),
		GatewayTLSCA:        os.Getenv("GLYPHOXA_WEB_GATEWAY_TLS_CA"),
		GatewaySharedSecret: os.Getenv("GLYPHOXA_WEB_GATEWAY_SECRET"),
		ListenAddr:          os.Getenv("GLYPHOXA_WEB_LISTEN_ADDR"),
	}

	if origins := os.Getenv("GLYPHOXA_WEB_ALLOWED_ORIGINS"); origins != "" {
		for _, o := range strings.Split(origins, ",") {
			o = strings.TrimSpace(o)
			if o != "" {
				cfg.AllowedOrigins = append(cfg.AllowedOrigins, o)
			}
		}
	}

	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8090"
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks that all required configuration values are set.
// At least one auth method must be configured: Discord OAuth2 or AdminAPIKey.
func (c *Config) Validate() error {
	var errs []error
	if c.DatabaseDSN == "" {
		errs = append(errs, fmt.Errorf("GLYPHOXA_WEB_DATABASE_DSN is required"))
	}
	if c.JWTSecret == "" {
		errs = append(errs, fmt.Errorf("GLYPHOXA_WEB_JWT_SECRET is required"))
	} else if len(c.JWTSecret) < 32 {
		errs = append(errs, fmt.Errorf("GLYPHOXA_WEB_JWT_SECRET must be at least 32 characters"))
	}

	// At least one auth method must be configured.
	hasDiscord := c.DiscordClientID != "" && c.DiscordClientSecret != "" && c.DiscordRedirectURI != ""
	hasGoogle := c.GoogleClientID != "" && c.GoogleClientSecret != "" && c.GoogleRedirectURI != ""
	hasGitHub := c.GitHubClientID != "" && c.GitHubClientSecret != "" && c.GitHubRedirectURI != ""
	hasAPIKey := c.AdminAPIKey != ""

	if !hasDiscord && !hasGoogle && !hasGitHub && !hasAPIKey {
		errs = append(errs, fmt.Errorf("at least one auth method required: set Discord/Google/GitHub OAuth2 vars or GLYPHOXA_WEB_ADMIN_KEY / GLYPHOXA_ADMIN_API_KEY"))
	}

	// mTLS: if any TLS field is set, all three must be set.
	tlsFields := []string{c.GatewayTLSCert, c.GatewayTLSKey, c.GatewayTLSCA}
	var hasAnyTLS bool
	for _, f := range tlsFields {
		if f != "" {
			hasAnyTLS = true
			break
		}
	}
	if hasAnyTLS {
		if c.GatewayTLSCert == "" || c.GatewayTLSKey == "" || c.GatewayTLSCA == "" {
			errs = append(errs, fmt.Errorf("when mTLS is configured, all three vars are required: GLYPHOXA_WEB_GATEWAY_TLS_CERT, _KEY, _CA"))
		}
	}

	return errors.Join(errs...)
}
