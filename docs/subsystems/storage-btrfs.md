# Storage: the btrfs model

hostit puts every app's home on a **btrfs subvolume**, and that one choice buys
four things that would otherwise be slow, approximate, or impossible: instant
crash-consistent snapshots, instant reflink forks, exact and cheap disk
accounting, and hard disk quotas enforced at write time. As of the current
release **btrfs is mandatory** -- the daemon refuses to start without it.

The btrfs primitives live in the `btrfs/` package (thin wrappers over the `btrfs`
CLI); the app-level orchestration is in `app/btrfs.go`, `app/snapshot.go`,
`app/quota.go`; the retention math is the pure `retention/` package.

```mermaid
flowchart TB
    subgraph fs["/var/lib/hostit/apps  (btrfs, a loopback image)"]
        subgraph subs["per-app home subvolumes (keyed on app id)"]
            h1["apps/&lt;id-A&gt;  (qgroup limit = disk_mb)"]
            h2["apps/&lt;id-B&gt;"]
        end
        subgraph snaps[".snapshots/&lt;id&gt;/  (read-only CoW)"]
            s1["&lt;id-A&gt;/auto-...    (hourly / pre-deploy)"]
            s2["&lt;id-A&gt;/manual-...  (owner / assistant)"]
        end
    end
    h1 -->|"btrfs subvolume snapshot -r<br/>(instant, space-shared)"| s1
    s2 -->|"writable snapshot (reflink)<br/>seed a fork or a rollback"| h2
    style snaps fill:#eff6ff,stroke:#3b82f6
```

## Per-app home subvolumes

A fresh app's home is created as a subvolume (`app/service.go:create` ->
`btrfs.CreateSubvolume`, which runs `btrfs subvolume create`). It is keyed on the
app id (`apps/<id>`), like everything durable -- see
[app-identity.md](app-identity.md). Snapshots live *beside* the home, not inside
it, under `apps/.snapshots/<id>/` (`app/btrfs.go:snapshotsRoot`), so a snapshot's
space is not charged against the app's own quota.

## Read-only snapshots

A snapshot is a copy-on-write, read-only subvolume: an instant, space-shared,
crash-consistent copy (`btrfs/service.go:Snapshot` with `-r`). hostit takes them:

- **hourly**, for every app (`app/snapshot.go:SnapshotLoop`, started from
  `cmd/serve.go`), labelled `"Automated snapshot"`,
- **before every deploy** (labelled `"Automated snapshot before deploy"`),
- **on demand**, labelled, by the owner or the assistant's `snapshot` tool
  (`app/snapshot.go:TakeSnapshot`),
- **as a safety snapshot** before a rollback.

`hostit.yml` may declare `snapshot.pre`/`snapshot.post` hooks that run **in the
container** to quiesce a database first; a failing `pre` hook **aborts** the
snapshot, so a torn state is never captured (`app/snapshot.go:takeSnapshot`).

### Rollback is stage-and-swap

Rollback never leaves the app without a home, even if it fails partway
(`app/snapshot.go:Rollback`):

1. **Stage** a writable copy of the target snapshot beside the home -- before
   touching the home, and before the safety snapshot (whose retention prune could
   otherwise delete the very target being restored).
2. Take a **safety snapshot** of the current state (so the rollback is itself
   undoable -- you can roll forward again).
3. Stop and remove the container, then **swap**: move the live home aside
   (`MoveSubvolume`, a same-fs metadata rename), move the staged copy in, and only
   then drop the old home. If putting the new one in place fails, the old one is
   moved back.
4. Restore ownership (`chown -R` to the app uid) and the qgroup quota, and start
   the app.

The per-app lifecycle lock (`app/service.go:lockApp`, `appLocks`) serializes
deploy/snapshot/rollback/delete, so these subvolume operations never interleave on
one home.

## Fork: seed a new app from a snapshot

Fork duplicates an app by seeding the new app's home from a **writable CoW
snapshot** of the source instead of the demo skeleton (`app/service.go:Fork` ->
`create` with a `seedPath`). The seed is the source's current home, or a named
snapshot of it; the reflink copy is instant and space-shared
(`btrfs.Snapshot(seedPath, home, false)` -- readonly=false makes it writable).
The forked home is then chowned to the new app's uid (it arrives owned by the
source's uid), and the new app gets its own port, uid block, user, subdomain,
container and a fresh agent token. Fork **requires** btrfs -- it is built on the
snapshot primitive, so `Fork` returns `ErrSnapshotsUnavailable` if the apps
filesystem is not btrfs.

## Hard disk quotas via qgroups

The disk limit is a btrfs **qgroup limit** on the home subvolume, equal to the
app's `disk_mb` (`app/quota.go:SetDiskLimit` -> `btrfs/service.go:SetQuota`, which
runs `btrfs qgroup limit`). This is a **hard** limit: a write past it fails with
**EDQUOT at write time**, not the periodic measure-and-stop that a soft quota
would need. Quota is applied at create and fork time (so a new app is capped from
the start) and re-applied after a rollback.

Usage accounting reads the qgroup too (`app/quota.go:measureDiskMB` ->
`btrfs.UsageMB`, parsing `btrfs qgroup show -f --raw`): accurate and cheap, no
directory walk. `RefreshDiskUsage` runs on an interval purely for the dashboard --
there is nothing to enforce, because the qgroup already hard-caps writes
(`app/quota.go`, `DiskUsageLoop` from `cmd/serve.go`).

## The retention engine (pure GFS)

Snapshots would accumulate forever, so a restic-style grandfather-father-son
policy thins them. It is a **pure function, no I/O**, so the bucketing math is
easy to test exhaustively (`retention/retention.go:Apply`). The default policy
(`retention.Default`): keep the last **50** snapshots outright, plus the newest in
each of the last **7 days**, **4 ISO weeks**, and **3 months**; the union is kept,
the rest pruned. Bucket keys are computed in **UTC** so retention is deterministic
regardless of the server's timezone.

Retention applies to **all** snapshots -- manual and automatic alike -- so none
lives forever (`retention.go` doc; `app/snapshot.go:pruneSnapshots` runs it after
each new snapshot and deletes both the subvolume and the DB row for each pruned
id). A prune that fails to delete a subvolume keeps the row, so it retries rather
than orphaning the subvolume.

```mermaid
flowchart LR
    all["all snapshots<br/>(newest first)"] --> last["keep last 50"]
    all --> daily["keep newest of<br/>each of last 7 days"]
    all --> weekly["keep newest of<br/>each of last 4 ISO weeks"]
    all --> monthly["keep newest of<br/>each of last 3 months"]
    last --> keep["union = keep"]
    daily --> keep
    weekly --> keep
    monthly --> keep
    all --> prune["everything else = prune<br/>(delete subvolume + row)"]
```

## btrfs is mandatory

Earlier hostit degraded gracefully on a non-btrfs host (plain directories, soft
quotas). That is gone: snapshots, rollback, fork and hard quotas are treated as
core, not optional. The startup preflight refuses to run otherwise
(`cmd/preflight.go:requireBtrfs`, called from `cmd/serve.go` after the apps
directory exists):

> hostit requires the app homes (...) to be on a btrfs filesystem, for snapshots,
> rollback and hard disk quotas

The Ansible role sets this up as a **loopback btrfs image** on the existing root
filesystem, so no extra block device is needed
(`deploy/ansible/roles/hostit/tasks/btrfs.yml`): it sizes an image at 75% of free
space, `mkfs.btrfs`, mounts it at the apps dir, enables quota, and rsyncs any
existing homes into subvolumes. See
[release-and-preflight.md](release-and-preflight.md).

### A note on the runtime btrfs check

The code still carries a runtime `btrfsEnabled()` cache (`app/btrfs.go`) and the
`ErrSnapshotsUnavailable` paths, from when btrfs was optional. With the mandatory
preflight in front of it, `btrfsEnabled()` is effectively always true in a real
deployment; the guards remain as defense in depth and to keep the fake-ops unit
tests (which run on whatever the test host's `/tmp` is) honest. Do not read the
presence of those guards as "btrfs is optional" -- the daemon will not start
without it.

## Where it lives

| Concern | Code |
|---|---|
| btrfs CLI wrappers (subvolume, snapshot, qgroup) | `btrfs/service.go` |
| home/snapshot path layout (keyed on app id) | `app/btrfs.go` |
| snapshot / rollback / prune orchestration | `app/snapshot.go` |
| quota set + usage accounting | `app/quota.go` |
| the GFS retention policy (pure, unit-tested) | `retention/retention.go` |
| the mandatory preflight | `cmd/preflight.go:requireBtrfs` |
| loopback btrfs setup | `deploy/ansible/roles/hostit/tasks/btrfs.yml` |
| snapshot metadata rows | `snapshot` table, `store/snapshot.go` |
