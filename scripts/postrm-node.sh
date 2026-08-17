#!/bin/sh
set -e

# postrm for hostit-node. Destructive cleanup happens only on `purge`, never on
# plain `remove` or `upgrade`, and it deliberately keeps /var/lib/hostit/apps
# and the app users: that is tenant data, and removing an app is the control
# plane's job (DELETE /api/apps/<name>), not the package's.

if [ "$1" = "purge" ] || [ "$1" = "0" ] 2>/dev/null; then
  rm -f /etc/hostit/node/node.yml.example
  rmdir /etc/hostit/node 2>/dev/null || true
  rm -f /etc/sudoers.d/hostit
  rmdir /etc/hostit 2>/dev/null || true
  if [ -f /etc/shells ]; then
    sed -i '\|^/usr/bin/hostit-shell$|d' /etc/shells || true
  fi
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi
