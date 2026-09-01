# Deploying hostit with Ansible

A small, self-contained example role for running hostit on Debian/Ubuntu. It
installs the component packages, writes each component's `/etc/hostit/<role>/…`
config, hardens sshd for app logins, puts app homes on btrfs, and starts the
services. An upgrade is just re-running it with a newer `hostit_version`.

hostit runs as three processes -- `hostit-control` (registry + decisions),
`hostit-node` (this machine's containers, users, subvolumes, port rules) and
`hostit-proxy` (TLS on :443, routing from the table control pushes it) -- and the
**same role covers every topology**. Which components a host runs is
`hostit_components` in the inventory:

- **Single box** -- one host, `[control, node, proxy]`. Members reach control
  over the `/run/hostit` unix socket; no cluster certs.
- **Split** -- a control+proxy frontend plus one or more remote `[node]` hosts
  that dial control over mTLS. Control opens a cluster listener; each remote node
  presents a CA-signed cert issued from your own CA (openssl recipe in
  `node.yml.example`), supplied via `hostit_cluster_ca_cert` + `hostit_cluster_certs`.

One process per job is the point: a control restart does not stop apps serving,
and adding a machine later is an inventory change, not a different architecture.

This is an **example** meant to be copied and adapted, not a published Galaxy role.

## Layout

```
deploy/ansible/
  playbook.yml                       # runs the role against the [hostit] group
  inventory/single-box.example.yml   # copy + edit: control+node+proxy on one host
  inventory/split.example.yml        # copy + edit: control+proxy frontend + remote nodes
  group_vars/hostit.example.yml      # copy to group_vars/hostit.yml, set secrets (use Vault)
  roles/hostit/                      # the role (defaults, tasks, handlers, templates)
```

`roles/hostit/defaults/main.yml` lists every variable, grouped **Common →
Control → Node → Proxy → Cluster**; the prefix tells you which component a
variable configures.

## Deploy

```
cp inventory/single-box.example.yml inventory/hosts.yml   # (or split.example.yml)
cp group_vars/hostit.example.yml    group_vars/hostit.yml # then edit both
ansible-playbook -i inventory/hosts.yml playbook.yml
```
