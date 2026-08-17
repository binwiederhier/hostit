#!/bin/sh
set -e

# postrm for hostit-control. On purge it drops the registry, certificates and
# session key -- everything directly under /var/lib/hostit -- but never
# /var/lib/hostit/apps, which is a colocated node's tenant data.

if [ "$1" = "purge" ] || [ "$1" = "0" ] 2>/dev/null; then
  find /var/lib/hostit -mindepth 1 -maxdepth 1 ! -name apps -exec rm -rf {} + 2>/dev/null || true
  rmdir /var/lib/hostit 2>/dev/null || true
  rm -f /etc/hostit/control/control.yml.example
  rmdir /etc/hostit/control 2>/dev/null || true
  rmdir /etc/hostit 2>/dev/null || true
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi
