//go:build integration

// End-to-end auth over a real Postgres (testcontainers): the OAuth callback
// issues a real session cookie, the Connect AuthService validates it through the
// interceptor stack, and Logout deletes the session row. Tag-isolated behind
// `integration` so the default `go test ./...` stays Docker-free (ADR-0021 /
// ADR-0033). The Postgres harness is duplicated in miniature (storage's
// harness_test.go is package-private).

package auth_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

const pgImage = "pgvector/pgvector:pg17"

func startPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, pgImage,
		tcpostgres.WithDatabase("glyphoxa_test"),
		tcpostgres.WithUsername("glyphoxa"),
		tcpostgres.WithPassword("glyphoxa"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Skipf("SKIPPED DB TEST — NO POSTGRES: could not start %s (is Docker running?): %v", pgImage, err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return dsn
}

func migratedStore(t *testing.T) (*storage.Store, *pgxpool.Pool) {
	t.Helper()
	dsn := startPostgres(t)
	ctx := context.Background()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.MigrateUp(ctx, db); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return storage.New(pool), pool
}

// cookieByName pulls one cookie out of a recorded response.
func cookieByName(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// TestAuthEndToEnd drives the whole single-operator flow against a real DB:
// OAuth callback → cookie issuance → GetCurrentUser validation → Logout deletes
// the session row.
func TestAuthEndToEnd(t *testing.T) {
	store, pool := migratedStore(t)
	ctx := context.Background()

	disc := &fakeDiscord{user: auth.DiscordUser{
		ID: "555", Username: "sora", GlobalName: "Sora Vance", AvatarURL: "https://cdn/a.png",
	}}
	// The operator snowflake is on the mandatory allowlist (ADR-0041) so the
	// callback issues a real session.
	o := auth.NewOAuth(store, disc, "/", auth.ParseOperatorAllowlist("555"), nil)

	// 1. OAuth callback issues a real session — capture the cookies.
	cbReq := httptest.NewRequest(http.MethodGet, "/auth/discord/callback?code=c&state=st", nil)
	cbReq.AddCookie(&http.Cookie{Name: "glyphoxa_oauth_state", Value: "st"})
	cbRec := httptest.NewRecorder()
	o.Callback(cbRec, cbReq)
	if cbRec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, body=%s", cbRec.Code, cbRec.Body.String())
	}
	sess := cookieByName(cbRec.Result().Cookies(), auth.SessionCookieName)
	csrf := cookieByName(cbRec.Result().Cookies(), auth.CSRFCookieName)
	if sess == nil || csrf == nil {
		t.Fatal("callback did not set session/csrf cookies")
	}

	// The session row exists for the upserted operator.
	var rowsBefore int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE token = $1`, sess.Value).Scan(&rowsBefore); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if rowsBefore != 1 {
		t.Fatalf("session rows = %d, want 1", rowsBefore)
	}

	// 2. AuthService over the real store + interceptor stack.
	stack := auth.NewStack(store, store, managementv1connect.AuthServiceGetCurrentUserProcedure)
	server := auth.NewAuthServer(store, store, store, auth.AdmissionAllowlist, nil)
	mux := http.NewServeMux()
	mux.Handle(server.Handler(stack.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := managementv1connect.NewAuthServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())

	cookieHeader := auth.SessionCookieName + "=" + sess.Value + "; " + auth.CSRFCookieName + "=" + csrf.Value

	// 3. GetCurrentUser with the cookie resolves the real operator.
	getReq := connect.NewRequest(&managementv1.GetCurrentUserRequest{})
	getReq.Header().Set("Cookie", cookieHeader)
	getResp, err := client.GetCurrentUser(ctx, getReq)
	if err != nil {
		t.Fatalf("GetCurrentUser: %v", err)
	}
	if got := getResp.Msg.GetUser(); got.GetName() != "Sora Vance" || got.GetAvatar() != "https://cdn/a.png" {
		t.Errorf("user = %+v, want Sora Vance", got)
	}

	// Missing cookie → 401.
	if _, err := client.GetCurrentUser(ctx, connect.NewRequest(&managementv1.GetCurrentUserRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("unauthenticated GetCurrentUser code = %v, want Unauthenticated", connect.CodeOf(err))
	}

	// 4. Logout (with CSRF double-submit) deletes the session row.
	logoutReq := connect.NewRequest(&managementv1.LogoutRequest{})
	logoutReq.Header().Set("Cookie", cookieHeader)
	logoutReq.Header().Set("X-CSRF-Token", csrf.Value)
	logoutResp, err := client.Logout(ctx, logoutReq)
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !strings.Contains(strings.Join(logoutResp.Header().Values("Set-Cookie"), " "), auth.SessionCookieName) {
		t.Error("Logout did not clear the session cookie")
	}

	var rowsAfter int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE token = $1`, sess.Value).Scan(&rowsAfter); err != nil {
		t.Fatalf("count sessions after logout: %v", err)
	}
	if rowsAfter != 0 {
		t.Fatalf("session rows after logout = %d, want 0 (logout must delete the row)", rowsAfter)
	}

	// The now-deleted session no longer authenticates.
	if _, err := store.AuthenticateSession(ctx, sess.Value); err == nil {
		t.Error("session still authenticates after logout")
	}
}
