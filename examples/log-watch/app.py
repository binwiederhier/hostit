"""Watch an app's logs, ask the model whether anything in them is serious, and
push a notification only when it says yes.

Two hostit ideas in one small app:

  * It asks a MODEL a question with no API key of its own, over hostit's own API
    (see judge()).
  * It sends the notification through a GRANTED ntfy credential, which it reads
    from the same API at the moment it needs it (see push()).

The interesting part is not the AI. It is that the alerting rule is written in
English rather than as a regex nobody maintains -- and that the app holds no
secret for either half of it.
"""

import json
import os
import time
import urllib.request

HOSTIT = os.environ.get("HOSTIT_API", "http://127.0.0.1:2586")
# Which app's logs to read, and which granted ntfy credential to shout through.
WATCH = os.environ.get("WATCH_APP", "")
NTFY = os.environ.get("NTFY_CONNECTION", "ntfy")
TOPIC = os.environ.get("NTFY_TOPIC", "")
EVERY = int(os.environ.get("INTERVAL_SECONDS", "300"))

RULE = """You triage application logs. You are woken only for things a person
must act on TONIGHT: data loss, a crash loop, an outage, an authentication
failure that looks like an attack. Routine errors, warnings, 404s, and one-off
failures that recovered are NOT worth waking anyone for.

Answer with one line: either IGNORE, or WAKE followed by a colon and one short
sentence saying what is wrong."""


def api_json(method, path, body=None):
    """Call hostit's container API at the loopback URL -- a plain HTTP request,
    no unix-socket client needed."""
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(HOSTIT + path, data=data, method=method,
                                 headers={"Content-Type": "application/json"} if data else {})
    with urllib.request.urlopen(req, timeout=120) as resp:
        return json.load(resp)


def logs(app, lines=200):
    """The app's recent output. Reading ANOTHER app's logs needs a token that
    can see it, so this is the one part that is not free -- see the README."""
    token = os.environ.get("HOSTIT_TOKEN", "")
    req = urllib.request.Request(
        f"{os.environ['HOSTIT_URL']}/api/apps/{app}/logs?lines={lines}",
        headers={"Authorization": f"Bearer {token}"})
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.load(r).get("output", "")


def judge(text):
    """Ask the model whether this is worth waking somebody for.

    max_tokens is small on purpose: the answer is one line, and every call is
    metered against this app.
    """
    return api_json("POST", "/api/container/assistant", {
        "system": RULE,
        "prompt": f"Log lines:\n\n{text[-6000:]}",
        "max_tokens": 100,
    })["text"].strip()


def push(title, message):
    """Send through a granted ntfy credential, read at the moment it is used."""
    tok = api_json("GET", f"/api/container/connections/{NTFY}/token")
    server = "https://ntfy.sh"
    topic = TOPIC
    for pair in (tok.get("meta") or "").split():
        key, _, value = pair.partition("=")
        if key == "server" and value:
            server = value
        if key == "topic" and value and not topic:
            topic = value
    req = urllib.request.Request(f"{server}/{topic}", data=message.encode(),
                                 headers={"Authorization": f"Bearer {tok['access_token']}",
                                          "Title": title, "Priority": "high"})
    urllib.request.urlopen(req, timeout=30).read()


def main():
    if not WATCH:
        raise SystemExit("set WATCH_APP to the app whose logs to watch")
    seen = ""
    while True:
        try:
            text = logs(WATCH)
            # Only judge what is NEW, so a problem that is already known does
            # not cost a call -- and a notification -- every five minutes.
            fresh = text[len(seen):] if text.startswith(seen) else text
            seen = text
            if fresh.strip():
                verdict = judge(fresh)
                print(f"verdict: {verdict}", flush=True)
                if verdict.upper().startswith("WAKE"):
                    push(f"{WATCH} needs you", verdict.partition(":")[2].strip() or verdict)
        except Exception as err:  # keep watching; a blip must not end the watch
            print(f"error: {err}", flush=True)
        time.sleep(EVERY)


if __name__ == "__main__":
    main()
