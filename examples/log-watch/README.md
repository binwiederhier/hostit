# Log watch

Reads another app's logs every few minutes, asks the model whether anything in
them is worth waking you for, and pushes a notification **only when it says yes**.

The interesting part is not the AI. It is that the alerting rule is written in
English:

> You are woken only for things a person must act on TONIGHT: data loss, a crash
> loop, an outage, an authentication failure that looks like an attack. Routine
> errors, warnings, 404s, and one-off failures that recovered are NOT worth
> waking anyone for.

Editing that sentence is the whole configuration surface. No regexes to maintain
and no threshold to tune.

## What it demonstrates

Two hostit ideas at once, and the app holds a secret for neither:

1. **Asking a model** over its own unix socket, with no API key
   (`/api/container/assistant`).
2. **Sending through a granted credential**, read at the moment it is used
   (`/api/container/connections/<name>/token`).

## Setup

1. Add an **ntfy** credential in hostit under Connections, and grant it to this
   app on the app's Connections tab.
2. Create an **account API token** (Profile -> API tokens). Reading *another*
   app's logs is the one thing the container socket cannot do for you -- the
   socket only ever speaks for the app it belongs to, which is the property that
   makes it safe.
3. Set the app's environment in `hostit.yml`:

```yaml
env:
  WATCH_APP: my-other-app
  HOSTIT_URL: https://apps.example.com
  HOSTIT_TOKEN: <your account token>
  NTFY_CONNECTION: ntfy       # the name you gave the credential
  NTFY_TOPIC: my-alerts
  INTERVAL_SECONDS: "300"
```

> A token in `env:` sits in `hostit.yml`, in the app's home. Use a token scoped
> as narrowly as you can, and see the TODO for the secrets store that will fix
> this properly.

## Cost

It judges only what is **new** since the last check, so a problem that is already
known does not cost a call -- or a notification -- every five minutes. `max_tokens`
is 100, because the answer is one line.
