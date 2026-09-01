#!/bin/sh
set -e

# postrm for hostit-proxy. On purge it drops the cached routes and
# certificates; they are a cache of control's state, never the source.

if [ "$1" = "purge" ] || [ "$1" = "0" ] 2>/dev/null; then
  rm -rf /var/lib/hostit-proxy
  rm -f /etc/hostit/proxy/proxy.yml.example
  rmdir /etc/hostit/proxy 2>/dev/null || true
  rmdir /etc/hostit 2>/dev/null || true
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi
