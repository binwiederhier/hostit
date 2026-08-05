#!/bin/sh
set -e

# postrm - runs after files are removed. Destructive cleanup happens only on
# `purge`, never on plain `remove` or `upgrade`. On purge we drop the registry
# and certs (everything directly under /var/lib/hostit) and the example config,
# but deliberately keep /var/lib/hostit/apps and the app users: that is
# operator/app data, and deleting users is the daemon's job (DELETE
# /api/apps/<name>), not the package's.

if [ "$1" = "purge" ] || [ "$1" = "0" ] 2>/dev/null; then
  # Remove the registry, certs and session key, but never the app homes under
  # apps/ -- purging the package must not destroy the tenants' data.
  find /var/lib/hostit -mindepth 1 -maxdepth 1 ! -name apps -exec rm -rf {} + 2>/dev/null || true
  rmdir /var/lib/hostit 2>/dev/null || true
  rm -f /etc/hostit/server.yml.example
  rm -f /etc/sudoers.d/hostit
  rmdir /etc/hostit 2>/dev/null || true
  if [ -f /etc/shells ]; then
    sed -i '\|^/usr/bin/hostit-shell$|d' /etc/shells || true
  fi
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi
