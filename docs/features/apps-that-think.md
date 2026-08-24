# Apps that think

## Description

An app asking a model a question **while it runs**, over hostit's own container
API -- a plain loopback URL (`http://127.0.0.1:2586`) or the unix socket it reads its
connections from -- with no API key of its own.

This is the opposite direction from the built-in assistant. That one BUILDS an
app: it reads and writes files, runs commands, deploys. This lets the app itself
think -- summarise its own logs and decide whether they are worth waking somebody
for, answer a visitor in a particular voice, classify some text.

```
GET  http://127.0.0.1:2586/api/container/assistant/models   # the ids this server offers
POST http://127.0.0.1:2586/api/container/assistant          # or --unix-socket /run/hostit/hostit.sock
  {"prompt": "...", "system": "...", "max_tokens": 500}
  {"messages": [{"role":"user","content":"..."}], "model": "claude-opus-5"}
->{"text": "...", "model": "...", "usage": {...}}
```

The model is the app's choice from what the instance has configured: a `claude-*`
id runs on the operator's Claude subscription, an `anthropic-*` id on the metered
API. `GET .../models` returns the available ids and the default; omit `model` to
take that default (the head of the catalog, same as the chat UI). The loopback
URL and the unix socket are the same API; the URL just lets an app use an
ordinary HTTP client instead of a unix-socket one.

## Why it exists

Because the alternative is a key in an app's environment, and that key is one
nobody can rotate, nobody can meter, and every process in that container can
read. It is the same argument the connections feature makes, applied to hostit's
own credential rather than a vendor's.

Going through hostit means the key stays on the server, every call is counted
against the app that made it, and the operator sees one usage number per app
whether it was spent by the chat or by the app itself.

**Answerence only, deliberately.** None of the assistant's tools are offered here.
The tools act ON an app -- write a file, run a command, deploy -- and an app that
could run them against itself is a self-modifying loop with nobody in the room.
An app that wants to change itself already has its own filesystem.

## User flows

Ask the assistant for it in plain English. The system prompt tells the model this
endpoint exists, so "build an app that reads my logs and pings me if anything
looks serious" produces something built on this rather than a suggestion to go
and get an API key.

```mermaid
sequenceDiagram
    participant App as App (in its container)
    participant H as hostit control
    participant M as model backend (API or Claude Max)
    App->>H: POST /api/container/assistant (unix socket, SO_PEERCRED)
    H->>H: resolve app from peer uid; reserve against OWNER's rate limit
    H->>M: route by model id -- metered API or Claude Max sandbox (NO tools)
    M-->>H: text + usage
    H->>H: record usage against this app
    H-->>App: {"text": ..., "usage": ...}
```

## Technical details

- `assistant/ask.go` -- `Manager.Ask` and `Manager.AskFor`. `AskFor` reserves
  against the owner's rate limit (`reserveRun`), the same one an interactive turn
  spends, because it is the same budget either way.
- `askOption` resolves the model id against `Catalog(creds)` -- the same catalog
  the chat UI offers, filtered to the backends this instance has configured --
  and `ask` routes on its backend: an `anthropic-*` id goes to the metered API
  (`m.client.complete`), a `claude-*` id to a one-shot tool-less `Sandbox.Answer`
  on the subscription. It is an ALLOWLIST, not a passthrough: an unknown or
  unconfigured id is refused with the list of what IS offered, since the id picks
  a paid backend. Omitting it takes `Default(creds)`, the head of the catalog --
  the same default a chat turn takes. `GET .../assistant/models`
  (`handleSelfAssistantModels`) is how an app discovers the ids without guessing.
- `validateAskMessages` bounds one request: `maxAskMessages` (60) and `maxAskChars`
  (200k). A chat app grows its history forever unless something says otherwise,
  and the first thing an unbounded history does is cost money.
- `max_tokens` is CLAMPED rather than refused. The app asked for something that
  sounds reasonable and should get an answer, just a bounded one.
- `control/server_handler_container_assistant.go` -- the endpoint. `prompt` and
  `messages` are alternatives and sending both is a 400: accepting both would
  silently drop one, and the app would be debugging a prompt that never went
  anywhere.
- Rate limiting answers **429 with the reason**, so an app written to back off
  can tell "too fast" apart from "broken".
- `agent/apiproxy.go` -- the in-container agent (PID 1) also serves this whole
  API on a loopback TCP address (`controlconf.ContainerAPIAddr`, `127.0.0.1:2586`)
  and reverse-proxies it to the unix socket, so an app can use a plain HTTP
  client. `SO_PEERCRED` identity is preserved: the agent connects as the
  container's root, which the idmap maps to the app's host uid -- the same uid
  the app's own process presents. The socket stays available; the loopback is an
  addition.

`assistant.NewClientAt` exists so this path can be driven end to end against a
stand-in Messages API. Testing through the real client rather than a stubbed
interface is what proves the JSON hostit sends and the usage it parses, instead
of only proving the handler calls something.

## Other notes

- **Availability** follows the assistant: whichever backends are configured --
  the metered API (`anthropic-api-key`), the Claude subscription
  (`claude-code-oauth-token`), or both. `GET .../models` is empty, and a call is
  `501`, only when neither is. The Claude backend runs a sandbox container per
  call, so it is heavier than the API -- fine for a periodic log check, slow for
  a chat making many calls.
- **No transcript is kept.** The model is stateless and hostit stores nothing per
  app here, so a chat sends its whole history each turn. That is why the message
  and character caps exist.
- **Usage lands on the app**, so it appears in the admin cost view beside what
  the chat spent. There is no separate budget for it yet -- worth adding if an
  app ever manages to spend meaningfully through the rate limit.
- **Known gap:** no streaming. A long answer arrives all at once, which is fine
  for a log summary and less good for a chat UI. Server-sent events would be the
  natural addition, and the interactive assistant already has that machinery.
