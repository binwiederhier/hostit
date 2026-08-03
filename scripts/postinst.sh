#!/bin/sh
set -e

# postinst - runs after files are extracted on .deb/.rpm install or upgrade.
# We only reload systemd and restart a running daemon; enabling the service
# is left to the operator (or Ansible), since hostit refuses to start without
# a configured /etc/hostit/server.yml anyway.

if [ "$1" = "configure" ] || [ "$1" -ge 1 ] 2>/dev/null; then
  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
    if systemctl is-active --quiet hostit 2>/dev/null; then
      systemctl restart hostit || true
    fi
  fi
  if [ ! -f /etc/hostit/server.yml ]; then
    echo "hostit: create /etc/hostit/server.yml (see /etc/hostit/server.yml.example)," >&2
    echo "hostit: then run: systemctl enable --now hostit" >&2
  fi
fi
