#!/bin/sh
set -e

# postinst for hostit-control. Restarts a running daemon and points a fresh
# install at its config; enabling the service is the operator's (or Ansible's).

if [ "$1" = "configure" ] || [ "$1" -ge 1 ] 2>/dev/null; then
  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
    if systemctl is-active --quiet hostit-control 2>/dev/null; then
      systemctl restart hostit-control || true
    fi
  fi
  if [ ! -f /etc/hostit/control/control.yml ]; then
    echo "hostit: create /etc/hostit/control/control.yml (see the example beside it)," >&2
    echo "hostit: then run: systemctl enable --now hostit-control" >&2
  fi
fi
