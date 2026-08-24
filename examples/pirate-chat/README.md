# Pirate chat

A chat page whose answers come from hostit's own model. **The app holds no API
key**, has no vendor account, and has no secret in its environment: it POSTs to
hostit's own API (`http://127.0.0.1:2586`, a plain loopback URL) and hostit makes the
call.

That is the whole point of the example. A key pasted into an app is a key nobody
can rotate, nobody can meter, and every process in that container can read.

## Run it

```
hostit apps add pirate-chat
scp app.py hostit.yml pirate-chat@<your-host>:
ssh pirate-chat@<your-host> hostit deploy
```

Nothing to configure and nothing to grant -- the endpoint is available to every
app whenever the server has the assistant set up.

## The part worth copying

```python
urllib.request.urlopen(urllib.request.Request(
    "http://127.0.0.1:2586/api/container/assistant",
    data=json.dumps({"system": PERSONA, "messages": messages}).encode(),
    headers={"Content-Type": "application/json"}))
```

An ordinary HTTP request to a normal URL -- no unix-socket client to hand-roll.
(The same API is on the socket at `/run/hostit/hostit.sock` if you prefer it.)

The model keeps nothing between calls, so a conversation is the whole history
sent each turn. Here the browser holds it and the app forwards it; hostit stores
none of it.

Two things the code does on purpose:

- **Trims the history** to the last 20 turns. hostit caps how much one request
  may carry, and a chat left open all day would grow past it.
- **Handles 429 as a sentence, not a stack trace.** Asking is rate limited
  against your account, because an app in a loop is the cheapest way to spend a
  month of budget by accident.
