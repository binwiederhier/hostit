#!/bin/sh
set -e

# postinst for hostit-node - runs after files are extracted on install or
# upgrade. It only prepares what app users need and restarts a running daemon;
# enabling the service is left to the operator (or Ansible), since a node
# refuses to start without a configured /etc/hostit/node/node.yml anyway.

if [ "$1" = "configure" ] || [ "$1" -ge 1 ] 2>/dev/null; then
  # Create the hostit-node system user and its /run dir from the shipped
  # sysusers.d/tmpfiles.d, so a bare "dpkg -i" is self-contained (no Ansible).
  # sysusers first: tmpfiles below chowns the dir to the user it creates.
  if command -v systemd-sysusers >/dev/null 2>&1; then
    systemd-sysusers /usr/lib/sysusers.d/hostit-node.conf || true
  fi
  if command -v systemd-tmpfiles >/dev/null 2>&1; then
    systemd-tmpfiles --create /usr/lib/tmpfiles.d/hostit-node.conf || true
  fi
  # Register the app-user login shell so chsh/sshd accept it, and drop the old
  # /usr/bin entry a previous release registered -- the shell moved off $PATH and
  # that binary no longer exists, so a stale /etc/shells line just misleads.
  if [ -f /etc/shells ]; then
    sed -i '\|^/usr/bin/hostit-shell$|d' /etc/shells || true
    if ! grep -qxF /usr/lib/hostit/bin/hostit-shell /etc/shells; then
      echo /usr/lib/hostit/bin/hostit-shell >> /etc/shells
    fi
  fi
  # The sudoers grant is scoped to this group; app users are added to it
  if ! getent group hostit-apps >/dev/null 2>&1; then
    groupadd --system hostit-apps
  fi
  # Never leave a broken sudoers file behind
  if [ -f /etc/sudoers.d/hostit ]; then
    if ! visudo -cqf /etc/sudoers.d/hostit 2>/dev/null; then
      echo "hostit: /etc/sudoers.d/hostit failed validation, removing it" >&2
      rm -f /etc/sudoers.d/hostit
    fi
  fi

  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
    if systemctl is-active --quiet hostit-node 2>/dev/null; then
      systemctl restart hostit-node || true
    fi
  fi
  if [ ! -f /etc/hostit/node/node.yml ]; then
    echo "hostit: create /etc/hostit/node/node.yml (see the example beside it)," >&2
    echo "hostit: then run: systemctl enable --now hostit-node" >&2
  fi
fi
