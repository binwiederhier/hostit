"""A personal dashboard that holds no credentials.

It asks hostit for a token per request over the app's own unix socket, uses it
against the vendor's API, and keeps nothing. Nothing here is configured with a
secret: no environment variable, no file, nothing in the image.
"""

import http.server
import json
import os
import socket
import urllib.request

SOCKET = "/run/hostit/hostit.sock"


def hostit(path):
    """GET one path from the hostit daemon over the app's unix socket."""
    conn = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    conn.settimeout(10)
    conn.connect(SOCKET)
    conn.sendall(f"GET {path} HTTP/1.1\r\nHost: hostit\r\nConnection: close\r\n\r\n".encode())
    raw = b""
    while chunk := conn.recv(65536):
        raw += chunk
    conn.close()
    head, _, body = raw.partition(b"\r\n\r\n")
    status = int(head.split()[1])
    return status, body.decode(errors="replace")


def github_login(token):
    """One read-only call, with the token hostit just handed us."""
    req = urllib.request.Request(
        "https://api.github.com/user",
        headers={"Authorization": "Bearer " + token, "User-Agent": "hostit-conndemo"},
    )
    with urllib.request.urlopen(req, timeout=15) as resp:
        return json.load(resp)


def render():
    lines = ["<h1>Connections demo</h1>"]
    status, body = hostit("/v1/connections")
    lines.append(f"<p>This app was granted: <code>{body.strip()}</code></p>")
    if status != 200:
        return "\n".join(lines + [f"<p>hostit said {status}</p>"])

    for provider in json.loads(body or "[]"):
        status, body = hostit(f"/v1/connections/{provider}/token")
        if status != 200:
            lines.append(f"<h2>{provider}</h2><p>hostit refused: {body.strip()}</p>")
            continue
        token = json.loads(body)
        # The token is used and dropped. It is never logged or rendered.
        lines.append(f"<h2>{provider}</h2>")
        if provider == "github":
            who = github_login(token["access_token"])
            lines.append(
                f"<p>Authenticated as <b>{who.get('login')}</b> "
                f"({who.get('public_repos')} public repos) &mdash; fetched live from api.github.com "
                f"with a credential this app never stored.</p>"
            )
        else:
            lines.append(f"<p>Connected: <code>{token.get('meta','')}</code></p>")
    return "\n".join(lines)


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        try:
            page = render()
        except Exception as err:  # a demo should show its failure, not a blank page
            page = f"<h1>Connections demo</h1><pre>{type(err).__name__}: {err}</pre>"
        out = f"<!doctype html><meta charset=utf-8><title>Connections demo</title><body style='font:16px system-ui;max-width:44rem;margin:3rem auto'>{page}</body>".encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def log_message(self, *args):
        pass


http.server.HTTPServer(("0.0.0.0", int(os.environ.get("PORT", 8080))), Handler).serve_forever()
