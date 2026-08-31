#!/bin/sh
set -e

# postinst for hostit-proxy. The proxy holds no state beyond its route/cert
# cache, so this only reloads systemd and restarts a running instance.

if [ "$1" = "configure" ] || [ "$1" -ge 1 ] 2>/dev/null; then
  # Create the hostit-proxy system user and its /run dir from the shipped
  # sysusers.d/tmpfiles.d, so a bare "dpkg -i" is self-contained (no Ansible).
  # sysusers first: tmpfiles below chowns the dir to the user it creates.
  if command -v systemd-sysusers >/dev/null 2>&1; then
    systemd-sysusers /usr/lib/sysusers.d/hostit-proxy.conf || true
  fi
  if command -v systemd-tmpfiles >/dev/null 2>&1; then
    systemd-tmpfiles --create /usr/lib/tmpfiles.d/hostit-proxy.conf || true
  fi
  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
    if systemctl is-active --quiet hostit-proxy 2>/dev/null; then
      systemctl restart hostit-proxy || true
    fi
  fi
fi
