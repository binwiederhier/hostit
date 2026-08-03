#!/bin/sh
set -e

# postrm - runs after files are removed. Destructive cleanup happens only on
# `purge`, never on plain `remove` or `upgrade`. On purge we drop the registry
# and certs (/var/lib/hostit) and the example config, but deliberately keep
# /srv/hostit/apps and the app users: that is operator/app data, and deleting
# users is the daemon's job (DELETE /v1/apps/<name>), not the package's.

if [ "$1" = "purge" ] || [ "$1" = "0" ] 2>/dev/null; then
  rm -rf /var/lib/hostit
  rm -f /etc/hostit/server.yml.example
  rmdir /etc/hostit 2>/dev/null || true
  if [ -f /etc/shells ]; then
    sed -i '\|^/usr/bin/hostit-shell$|d' /etc/shells || true
  fi
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi
