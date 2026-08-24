"""A chat page whose answers come from hostit's own assistant endpoint.

The part worth copying is ask(): the app has NO API key, no vendor account and
no secret in its environment. It POSTs to hostit's own API and hostit makes the
call, which is why the key can be rotated without touching this app and why what
it spends shows up per app rather than on one anonymous bill.

hostit's API is reachable at a plain loopback URL (http://127.0.0.1:2586), so this is
an ordinary HTTP request -- no unix-socket client to hand-roll. (The same API is
also on the socket at /run/hostit/hostit.sock if you prefer it.)

The model is stateless, so a conversation is the whole history sent each turn.
The browser keeps it; the server keeps nothing.
"""

import json
import os
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer

HOSTIT = os.environ.get("HOSTIT_API", "http://127.0.0.1:2586")
PERSONA = "You are a helpful assistant who answers as a pirate. Keep it to a few sentences."


def ask(messages):
    """Send a conversation to hostit and return the model's reply.

    Errors are returned as text rather than raised: this is a chat, and a page
    that says what went wrong is more useful than one that shows a stack trace.
    """
    body = json.dumps({"system": PERSONA, "messages": messages, "max_tokens": 500}).encode()
    req = urllib.request.Request(HOSTIT + "/api/container/assistant", data=body,
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            return json.loads(resp.read()).get("text", "")
    except urllib.error.HTTPError as err:
        if err.code == 429:
            return "Slow down there, matey -- too many questions at once. Try again in a moment."
        return f"(the model could not be reached: HTTP {err.code} {err.read()[:200].decode(errors='replace')})"
    except urllib.error.URLError as err:
        return f"(the model could not be reached: {err})"


PAGE = """<!doctype html>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Pirate chat</title>
<style>
 body{font:16px/1.5 system-ui,sans-serif;max-width:44rem;margin:2rem auto;padding:0 1rem}
 #log{display:flex;flex-direction:column;gap:.75rem;margin:1.5rem 0}
 .m{padding:.6rem .9rem;border-radius:.8rem;max-width:80%;white-space:pre-wrap}
 .you{align-self:flex-end;background:#dbeafe}
 .them{align-self:flex-start;background:#f3f4f6}
 form{display:flex;gap:.5rem} input{flex:1;padding:.6rem;font:inherit}
 button{padding:.6rem 1rem;font:inherit}
</style>
<h1>Pirate chat</h1>
<p>Answers come from hostit's own model. This app holds no API key.</p>
<div id="log"></div>
<form><input id="q" autofocus placeholder="Ask something..." autocomplete="off"><button>Send</button></form>
<script>
// The conversation lives here, in the browser: the model is stateless and the
// app stores nothing, so every turn sends the whole history.
const history = [];
const log = document.getElementById("log");
function add(role, text) {
  const el = document.createElement("div");
  el.className = "m " + (role === "user" ? "you" : "them");
  el.textContent = text;
  log.appendChild(el);
  el.scrollIntoView({block: "end"});
}
document.querySelector("form").onsubmit = async (e) => {
  e.preventDefault();
  const input = document.getElementById("q");
  const text = input.value.trim();
  if (!text) return;
  input.value = "";
  add("user", text);
  history.push({role: "user", content: text});
  const res = await fetch("/chat", {method: "POST", body: JSON.stringify(history)});
  const reply = (await res.json()).text;
  add("assistant", reply);
  history.push({role: "assistant", content: reply});
};
</script>
"""


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send(200, "text/html; charset=utf-8", PAGE.encode())

    def do_POST(self):
        if self.path != "/chat":
            return self.send(404, "text/plain", b"not found")
        length = int(self.headers.get("Content-Length", 0))
        messages = json.loads(self.rfile.read(length) or b"[]")
        # Trimmed to the last few turns: hostit caps how much an app may send,
        # and a chat left open all day would otherwise grow past it.
        self.send(200, "application/json",
                  json.dumps({"text": ask(messages[-20:])}).encode())

    def send(self, status, ctype, body):
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass  # the platform already logs requests


if __name__ == "__main__":
    HTTPServer(("", int(os.environ.get("PORT", 8080))), Handler).serve_forever()
