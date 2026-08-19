// reconnectDelaySeconds is the backoff before the Nth reconnect attempt (0-indexed):
// 1, 2, 4, 8, ... doubling, capped at 60 seconds, so a dropped terminal keeps
// retrying without hammering the server.
export function reconnectDelaySeconds(attempt) {
  return Math.min(60, Math.pow(2, Math.max(0, attempt)));
}

// TERMINAL_POWERED_OFF_CODE is the WebSocket close code the daemon sends when the
// app is powered off (server: terminalStatusPoweredOff). It is in the WebSocket
// application-private code range (4000-4999).
export const TERMINAL_POWERED_OFF_CODE = 4001;

// TERMINAL_ARCHIVED_CODE is the close code for an archived app (server:
// terminalStatusArchived). Archiving is even more final than a poweroff: the app
// refuses to start at all until it is unarchived, so retrying is pointless.
export const TERMINAL_ARCHIVED_CODE = 4002;

// shouldReconnect decides whether a terminal drop should trigger a reconnect. A
// powered-off close is deliberate and final until the operator powers the app back
// on, so it must NOT reconnect: a reconnect would be refused and must never power
// the app back on. Every other close (a network blip, a container restart) heals by
// reconnecting.
export function shouldReconnect(closeCode) {
  return closeCode !== TERMINAL_POWERED_OFF_CODE && closeCode !== TERMINAL_ARCHIVED_CODE;
}
