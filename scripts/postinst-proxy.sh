#!/bin/sh
set -e

# postinst for hostit-proxy. The proxy holds no state beyond its route/cert
# cache, so this only reloads systemd and restarts a running instance.

if [ "$1" = "configure" ] || [ "$1" -ge 1 ] 2>/dev/null; then
  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
    if systemctl is-active --quiet hostit-proxy 2>/dev/null; then
      systemctl restart hostit-proxy || true
    fi
  fi
fi
