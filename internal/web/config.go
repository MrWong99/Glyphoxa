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
)

// Config holds all configuration for the web management service.
// Values are loaded from environment variables.
type Config struct {
	// DatabaseDSN is the PostgreSQL connection string.
	DatabaseDSN string

	// JWTSecret is the HMAC key used to sign and verify JWTs.
	JWTSecret string

	// DiscordClientID is the Discord OAuth2 application client ID.
	DiscordClientID string

	// DiscordClientSecret is the Discord OAuth2 application client secret.
	DiscordClientSecret string

	// DiscordRedirectURI is the OAuth2 callback URL registered with Discord.
	DiscordRedirectURI string

	// GatewayURL is the base URL of the gateway's internal admin API.
	GatewayURL string

	// ListenAddr is the address the HTTP server binds to.
	ListenAddr string
}

// LoadConfig reads configuration from environment variables and validates
// that all required values are present.
func LoadConfig() (*Config, error) {
	cfg := &Config{
		DatabaseDSN:         os.Getenv("GLYPHOXA_WEB_DATABASE_DSN"),
		JWTSecret:           os.Getenv("GLYPHOXA_WEB_JWT_SECRET"),
		DiscordClientID:     os.Getenv("GLYPHOXA_WEB_DISCORD_CLIENT_ID"),
		DiscordClientSecret: os.Getenv("GLYPHOXA_WEB_DISCORD_CLIENT_SECRET"),
		DiscordRedirectURI:  os.Getenv("GLYPHOXA_WEB_DISCORD_REDIRECT_URI"),
		GatewayURL:          os.Getenv("GLYPHOXA_WEB_GATEWAY_URL"),
		ListenAddr:          os.Getenv("GLYPHOXA_WEB_LISTEN_ADDR"),
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
	if c.DiscordClientID == "" {
		errs = append(errs, fmt.Errorf("GLYPHOXA_WEB_DISCORD_CLIENT_ID is required"))
	}
	if c.DiscordClientSecret == "" {
		errs = append(errs, fmt.Errorf("GLYPHOXA_WEB_DISCORD_CLIENT_SECRET is required"))
	}
	if c.DiscordRedirectURI == "" {
		errs = append(errs, fmt.Errorf("GLYPHOXA_WEB_DISCORD_REDIRECT_URI is required"))
	}
	return errors.Join(errs...)
}
