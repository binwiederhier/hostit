#!/bin/sh
set -e

# postinst for hostit-control. Restarts a running daemon and points a fresh
# install at its config; enabling the service is the operator's (or Ansible's).

if [ "$1" = "configure" ] || [ "$1" -ge 1 ] 2>/dev/null; then
  # Create the hostit-control system user and its /run dir from the shipped
  # sysusers.d/tmpfiles.d, so a bare "dpkg -i" is self-contained (no Ansible).
  # sysusers first: tmpfiles below chowns the dir to the user it creates.
  if command -v systemd-sysusers >/dev/null 2>&1; then
    systemd-sysusers /usr/lib/sysusers.d/hostit-control.conf || true
    # The SSH-relay stub group; the relay reconcile puts stub accounts in it.
    systemd-sysusers /usr/lib/sysusers.d/hostit-relay.conf || true
  fi
  if command -v systemd-tmpfiles >/dev/null 2>&1; then
    systemd-tmpfiles --create /usr/lib/tmpfiles.d/hostit-control.conf || true
  fi
  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
    if systemctl is-active --quiet hostit-control 2>/dev/null; then
      systemctl restart hostit-control || true
    fi
  fi
  if [ ! -f /etc/hostit/control/control.yml ]; then
    echo "hostit: create /etc/hostit/control/control.yml (see the example beside it)," >&2
    echo "hostit: then run: systemctl enable --now hostit-control" >&2
    echo "hostit: the config holds secrets -- chown hostit-control:hostit-control and chmod 600 it" >&2
  fi
fi
