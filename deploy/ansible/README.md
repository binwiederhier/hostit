# Deploying hostit with Ansible

A small, self-contained example role for running hostit on a single Linux host
(Debian or Ubuntu). It installs the three component packages, writes
`/etc/hostit/control/control.yml` and `/etc/hostit/proxy/proxy.yml`, hardens
sshd for app logins, optionally puts app homes on btrfs, and starts the
services. An upgrade is just re-running it with a newer `hostit_version`.

hostit ships one package per component, and runs them as three processes even
on one machine: `hostit-control` (the registry and the decisions),
`hostit-node` (this machine's app work: containers, users, subvolumes, port
rules) and `hostit-proxy` (TLS on :443, routing from the table control pushes
it). They talk over one mTLS connection each member dials; control never dials
back.

One process per job is the point. A control restart does not stop apps serving,
and adding a second machine later is a node config on that machine rather than a
different architecture. The "Deployment shapes" section of the administration
guide (`/docs/admin#deployment`, source in `web/src/pages/Docs.jsx`) has the
multi-machine version.

**Upgrading from a hostit before the package split**: the role stops and removes
the old single `hostit` service and package, installs the three, and starts them
in order. Your data and `/etc/hostit/server.yml` are left alone; the new
per-component configs live under `/etc/hostit/<component>/`. Expect a short
interruption while control migrates the registry and the apps restart onto the
new build.

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
