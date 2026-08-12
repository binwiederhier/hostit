// reconnectDelaySeconds is the backoff before the Nth reconnect attempt (0-indexed):
// 1, 2, 4, 8, ... doubling, capped at 60 seconds, so a dropped terminal keeps
// retrying without hammering the server.
export function reconnectDelaySeconds(attempt) {
  return Math.min(60, Math.pow(2, Math.max(0, attempt)));
}
