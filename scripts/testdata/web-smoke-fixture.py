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
assertion actually gates:
  placeholder | login_status | login_location | login_cookie
  | callback_accepts | getcurrentuser_open | protected_rpc_open
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

AUTHORIZE_URL = "https://discord.com/api/oauth2/authorize"


def authorize_location():
    q = urlencode(
        {
            "client_id": CLIENT_ID,
            "redirect_uri": REDIRECT_URL,
            "response_type": "code",
            "scope": "identify",
            "state": STATE,
        }
    )
    return AUTHORIZE_URL + "?" + q


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
        path = self.path.split("?", 1)[0]
        if path == "/":
            self._body(200, PLACEHOLDER if BREAK == "placeholder" else REAL_CONSOLE)
            return
        if path == "/auth/discord/login":
            self._login()
            return
        if path == "/auth/discord/callback":
            # A real callback with a forged/missing state is a 400; the "accepts"
            # break lets the forgery through (200) so the assertion must catch it.
            self._body(200 if BREAK == "callback_accepts" else 400, "callback")
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
        location = (
            "https://evil.example/authorize?response_type=code&scope=identify"
            if BREAK == "login_location"
            else authorize_location()
        )
        headers = [("Location", location)]
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


def main():
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 18099
    HTTPServer(("127.0.0.1", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
