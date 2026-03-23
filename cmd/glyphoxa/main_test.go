package main

import (
	"os"
	"testing"
)

func TestApplySSLMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		dsn     string
		envMode string // GLYPHOXA_DATABASE_SSLMODE value; empty means unset
		want    string
	}{
		{
			name: "URL without sslmode gets prefer by default",
			dsn:  "postgres://user:pass@localhost:5432/db",
			want: "postgres://user:pass@localhost:5432/db?sslmode=prefer",
		},
		{
			name: "URL with existing query params",
			dsn:  "postgres://user:pass@localhost:5432/db?connect_timeout=5",
			want: "postgres://user:pass@localhost:5432/db?connect_timeout=5&sslmode=prefer",
		},
		{
			name: "URL already has sslmode — unchanged",
			dsn:  "postgres://user:pass@localhost:5432/db?sslmode=disable",
			want: "postgres://user:pass@localhost:5432/db?sslmode=disable",
		},
		{
			name:    "URL with custom env sslmode",
			dsn:     "postgres://user:pass@localhost:5432/db",
			envMode: "require",
			want:    "postgres://user:pass@localhost:5432/db?sslmode=require",
		},
		{
			name: "key-value without sslmode",
			dsn:  "host=localhost port=5432 user=user dbname=db",
			want: "host=localhost port=5432 user=user dbname=db sslmode=prefer",
		},
		{
			name: "key-value already has sslmode — unchanged",
			dsn:  "host=localhost sslmode=disable dbname=db",
			want: "host=localhost sslmode=disable dbname=db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: mutates env var.
			if tt.envMode != "" {
				t.Setenv("GLYPHOXA_DATABASE_SSLMODE", tt.envMode)
			} else {
				os.Unsetenv("GLYPHOXA_DATABASE_SSLMODE")
			}

			got := applySSLMode(tt.dsn)
			if got != tt.want {
				t.Errorf("applySSLMode(%q) = %q, want %q", tt.dsn, got, tt.want)
			}
		})
	}
}
