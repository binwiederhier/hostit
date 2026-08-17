# Deploying hostit with Ansible

A small, self-contained example role for running hostit on a single Linux host
(Debian or Ubuntu). It installs the three component packages, writes
`/etc/hostit/control/control.yml` and `/etc/hostit/proxy/proxy.yml`, hardens
sshd for app logins, optionally puts app homes on btrfs, and starts the
services. An upgrade is just re-running it with a newer `hostit_version`.

hostit ships one package per component -- `hostit-control` (the brain),
`hostit-proxy` (the TLS-terminating data plane) and `hostit-node` (the machine
half). On a single box this role installs all three but runs only control and
the proxy: control does the machine work itself, and the node package is there
for the `hostit` CLI that gets bind-mounted into every app container, the
per-app systemd unit, the app login shell and the sudoers grant.

Splitting the machine half onto its own hosts is a matter of config, not a
different role -- the "Deployment shapes" section of the administration guide
(`/docs/admin#deployment`, source in `web/src/pages/Docs.jsx`) has a worked
three-machine example.

This is an **example** meant to be copied and adapted, not a published Galaxy
role.

## Layout

```
deploy/ansible/
  playbook.yml                 # runs the role against the [hostit] group
  inventory.example.ini        # copy to inventory.ini, set your host
  group_vars/hostit.example.yml # copy to group_vars/hostit.yml, set vars
  roles/hostit/                # the role (defaults, tasks, handlers, templates)
```

## Use

1. Point DNS at the host (both records are required):

   ```
   apps.example.com.    A  <host-ip>
   *.apps.example.com.  A  <host-ip>
   ```

2. Copy and edit the inventory and vars:

   ```sh
   cp inventory.example.ini inventory.ini
   cp group_vars/hostit.example.yml group_vars/hostit.yml
   $EDITOR inventory.ini group_vars/hostit.yml
   ```

   At minimum set `hostit_domain` and `hostit_admin_token`. Put secrets (the admin
   token, the OAuth secret, any AI keys) in an Ansible Vault file rather than in
   plain vars.

3. Run it:

   ```sh
   ansible-playbook -i inventory.ini playbook.yml
   # or, with a vault:
   ansible-playbook -i inventory.ini playbook.yml --ask-vault-pass
   ```

## Notes

- **btrfs is optional.** `hostit_btrfs: true` puts app homes on a btrfs loopback so
  hostit can snapshot them and enforce hard disk quotas; it is a one-off migration.
  Without it apps still run, you just lose snapshots, rollback, fork and hard
  quotas. Recommended for production.
- **sshd hardening.** The role drops a `Match Group hostit-apps` block into
  `/etc/ssh/sshd_config.d/` that disables all forwarding for app logins (an app
  session must not become a tunnel into the host). It restarts sshd; existing
  admin sessions are unaffected.
- **The daemon runs as root** because it creates Unix users and drives podman,
  systemd, nftables and btrfs. App containers are isolated; the daemon is the
  trusted control plane, so keep the host itself locked down.
- **Local build.** To deploy locally built packages instead of a release, set
  `hostit_deb_dir` to the `dist/` directory `make release-snapshot` produced,
  and `hostit_deb_version` to the version they carry.
- **Upgrading from before the package split.** The three packages replace the
  old single `hostit` package; the role removes it, and its config and data are
  left alone. `/etc/hostit/server.yml` is superseded by the per-component files
  and can be deleted once the new ones are in place.

For the full list of options, see `control.yml.example` and `proxy.yml.example`
in the repository root, or the "Server configuration" section of the in-app docs
(`/docs`).
