#!/bin/sh
set -e

# postinst - runs after files are extracted on .deb/.rpm install or upgrade.
# We only reload systemd and restart a running daemon; enabling the service
# is left to the operator (or Ansible), since hostit refuses to start without
# a configured /etc/hostit/server.yml anyway.

if [ "$1" = "configure" ] || [ "$1" -ge 1 ] 2>/dev/null; then
  # Register the app-user login shell so chsh/sshd accept it
  if [ -f /etc/shells ] && ! grep -qxF /usr/bin/hostit-shell /etc/shells; then
    echo /usr/bin/hostit-shell >> /etc/shells
  fi
  # The sudoers grant is scoped to this group; app users are added to it
  if ! getent group hostit-apps >/dev/null 2>&1; then
    groupadd --system hostit-apps
  fi

  # Never leave a broken sudoers file behind
  if [ -f /etc/sudoers.d/hostit ]; then
    chmod 0440 /etc/sudoers.d/hostit || true
    chown root:root /etc/sudoers.d/hostit || true
    if command -v visudo >/dev/null 2>&1 && ! visudo -cf /etc/sudoers.d/hostit >/dev/null 2>&1; then
      echo "hostit: warning: /etc/sudoers.d/hostit failed validation; removing it" >&2
      rm -f /etc/sudoers.d/hostit
    fi
  fi

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
