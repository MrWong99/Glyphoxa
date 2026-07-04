#!/usr/bin/env python3
"""Fixture web tier for scripts/e2e-deploy-smoke-test.sh (issue #128).

Serves the SAME contract the real cluster-served Web Instance does for the
web-http assertion subset of scripts/e2e-deploy-smoke.sh, so those assertions
can be exercised without a cluster:

  GET  /                          -> 200, the real console shell (a hashed
                                     /assets/index-<hash>.js reference), NOT the
                                     committed placeholder index.html one-liner.
  GET  /auth/discord/login        -> 302 to the Discord authorize URL carrying
                                     the configured client id + redirect, and a
                                     glyphoxa_oauth_state cookie. No Discord call.
  GET  /auth/discord/callback     -> 400 (a bare request carries no valid state).
  POST .../AuthService/GetCurrentUser        -> 401 (the public probe's by-design
                                                signal for "no session").
  POST .../CampaignService/GetActiveCampaign -> 401 (a protected RPC refuses a
                                                request with no session cookie).

$FIXTURE_BREAK breaks exactly ONE property so the self-test can prove each
assertion actually gates on its OWN dedicated mutation (a break that trips two
properties at once would let either assertion be deleted unnoticed):
  placeholder              -> served root is the placeholder (no hashed asset)
  login_status             -> login answers 200, not a 302
  login_wrong_host         -> login 302s somewhere that is not Discord authorize
  login_wrong_client_id    -> login redirect carries the wrong client_id
  login_wrong_redirect_uri -> login redirect carries the wrong redirect_uri
  login_cookie             -> login redirect sets no state cookie
  callback_accepts_forged  -> callback accepts a forged (mismatched) state
  callback_accepts_missing -> callback accepts a missing state
  getcurrentuser_open      -> unauthenticated GetCurrentUser answers 200
  protected_rpc_open       -> a protected RPC answers 200 without a session
"""
import os
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlencode

# Mirror the CI dummy OAuth values scripts/e2e-deploy-smoke.sh installs + asserts.
CLIENT_ID = "ci-not-a-real-oauth-client-id"
REDIRECT_URL = "http://localhost:8080/auth/discord/callback"
STATE = "fixture-state-nonce"

BREAK = os.environ.get("FIXTURE_BREAK", "")

REAL_CONSOLE = (
    "<!doctype html><html lang=\"en\"><head><meta charset=\"UTF-8\"/>"
    "<script type=\"module\" crossorigin src=\"/assets/index-a1b2c3d4.js\"></script>"
    "<link rel=\"stylesheet\" href=\"/assets/index-e5f6a7b8.css\"/></head>"
    "<body><div id=\"root\"></div></body></html>"
)
# The committed placeholder index.html, verbatim (internal/spa/dist/index.html).
PLACEHOLDER = '<!doctype html><html><body><div id="root"></div></body></html>'

DISCORD_AUTHORIZE_URL = "https://discord.com/api/oauth2/authorize"


def authorize_location(base=DISCORD_AUTHORIZE_URL, client_id=CLIENT_ID, redirect=REDIRECT_URL):
    q = urlencode(
        {
            "client_id": client_id,
            "redirect_uri": redirect,
            "response_type": "code",
            "scope": "identify",
            "state": STATE,
        }
    )
    return base + "?" + q


def login_location():
    if BREAK == "login_wrong_host":
        return authorize_location(base="https://evil.example/api/oauth2/authorize")
    if BREAK == "login_wrong_client_id":
        return authorize_location(client_id="some-other-app-id")
    if BREAK == "login_wrong_redirect_uri":
        return authorize_location(redirect="http://evil.example/steal")
    return authorize_location()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def _body(self, code, text, extra_headers=None):
        payload = text.encode()
        self.send_response(code)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(payload)))
        for k, v in (extra_headers or []):
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self):
        path, _, query = self.path.partition("?")
        if path == "/":
            self._body(200, PLACEHOLDER if BREAK == "placeholder" else REAL_CONSOLE)
            return
        if path == "/auth/discord/login":
            self._login()
            return
        if path == "/auth/discord/callback":
            self._callback(query)
            return
        self._body(404, "not found")

    def do_POST(self):
        path = self.path.split("?", 1)[0]
        if path.endswith("AuthService/GetCurrentUser"):
            self._body(200 if BREAK == "getcurrentuser_open" else 401, "{}")
            return
        if path.endswith("CampaignService/GetActiveCampaign"):
            self._body(200 if BREAK == "protected_rpc_open" else 401, "{}")
            return
        self._body(404, "not found")

    def _login(self):
        if BREAK == "login_status":
            self._body(200, "no redirect")
            return
        headers = [("Location", login_location())]
        if BREAK != "login_cookie":
            headers.append(
                (
                    "Set-Cookie",
                    "glyphoxa_oauth_state=%s; Path=/; HttpOnly; SameSite=Lax" % STATE,
                )
            )
        self.send_response(302)
        for k, v in headers:
            self.send_header(k, v)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def _callback(self, query):
        # The real callback 400s a forged (mismatched-state) AND a missing-state
        # request, both BEFORE any Discord exchange. Each "accepts" break lets
        # exactly ONE of those through (200) so its assertion has its own mutation.
        has_state = "state=" in query
        if BREAK == "callback_accepts_forged":
            code = 200 if has_state else 400
        elif BREAK == "callback_accepts_missing":
            code = 200 if not has_state else 400
        else:
            code = 400
        self._body(code, "callback")


def main():
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 18099
    HTTPServer(("127.0.0.1", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
