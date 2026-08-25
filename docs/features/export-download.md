# Export and download

## Description

An owner can download an app's files as a single archive -- a `.zip` or a
`.tar.gz` -- straight from the browser, with no SSH, scp or local tooling. There
are two things to download:

- **The live workspace.** A download icon in the app detail header (next to the
  fork icon) drops a small menu: "Download as .zip" / "Download as .tar.gz". On
  a narrow screen it folds into the kebab (...) actions menu, under "Fork app",
  as an expandable "Download workspace" row with the same two choices. The
  archive is a point-in-time copy of the app's files even while the app keeps
  writing, because hostit takes a consistent read-only snapshot first, archives
  that, and drops it.
- **One snapshot.** Every row on the Snapshots page has the same download icon
  and the same `.zip` / `.tar.gz` menu. It archives that snapshot's files
  directly, so it is exactly what the app looked like when the snapshot was
  taken.

Either way the browser saves a file named after the app: `<app>.zip` /
`<app>.tar.gz` for the live workspace, and `<app>-<snapshotid>.zip` /
`.tar.gz` for a snapshot.

## Why it exists

The workspace already lives on the server, reachable over the file API, SSH and
snapshots, but there was no one-click way to walk away with the whole thing --
to keep a local backup, hand the app to someone off-platform, or open it in a
local editor. Export closes that: it turns "the files are on hostit" into "the
files are also a download".

Two decisions are worth recording:

- **The live export snapshots first.** Archiving a directory a running app is
  still writing to would capture a torn tree (a half-written file, a database
  mid-flush). Taking a read-only btrfs snapshot first makes the archive a
  consistent instant, and btrfs makes that snapshot almost free, so the export
  never has to pause or lock the app. A per-snapshot download needs none of this:
  a snapshot is already an immutable point-in-time copy, so it is archived
  straight from its subvolume with no new snapshot taken.
- **Two formats, zip as the default.** `.zip` opens everywhere with no tooling,
  so it is the default; `.tar.gz` is leaner and preserves unix modes and
  symlinks, so it is offered alongside for anyone who wants a faithful copy.

## User flows

Downloading the live workspace from the app page:

```mermaid
sequenceDiagram
    actor Owner
    participant Web as App page
    participant Control as hostit-control
    participant Node as hostit-node
    participant btrfs
    Owner->>Web: Download -> "as .zip"
    Web->>Control: GET /api/apps/{app}/export
    Control->>Node: ArchiveWorkspace(app, zip)
    Node->>btrfs: read-only snapshot .export-<rand>
    Node-->>Control: archive stream
    Control-->>Web: 200, attachment; filename="<app>.zip"
    Node->>btrfs: delete .export-<rand> (stream ended)
    Web-->>Owner: browser saves <app>.zip
```

Downloading one snapshot is the same, minus the transient snapshot: the row's
download menu points at `GET /api/apps/{app}/snapshots/{id}/export`, which walks
the existing snapshot subvolume straight into the stream.

## Technical details

HTTP surface (agent/API, shared by the browser session and an app-scoped token):

- `GET /api/apps/{app}/export[?format=tar]` -- the live workspace.
- `GET /api/apps/{app}/snapshots/{id}/export[?format=tar]` -- one snapshot.
- Default format is `zip`; `format=tar` gives a gzipped tar. Both are registered
  on the agent routes behind `requireApp`
  (`control/server_handler_agent.go:newAgentRoutes`), so the same authorization
  as every other per-app endpoint applies.

Handlers (`control/server_handler_files.go`):

- `handleAppExport` calls `s.node.ArchiveWorkspace`; `handleSnapshotExport` calls
  `s.node.ArchiveSnapshot` with the `{id}` path value; `exportFormat` reads
  `?format=`.
- `streamArchive` writes the read-closer to the response as a download: it sets
  `Content-Type` (`application/zip` or `application/gzip`), `X-Content-Type-Options:
  nosniff`, and `Content-Disposition: attachment; filename="<base>.<ext>"` where
  `<base>` is `<app>` or `<app>-<id>`. A reportable error (app missing, unknown
  snapshot id) surfaces before the first byte; an unknown snapshot id maps to 404
  (`store.ErrSnapshotNotFound`), everything else through `writeAppError`. A
  failure mid-copy just truncates the download -- it cannot be reported once
  streaming has begun.

Node side (`node/machine_archive.go`, routed by
`control/registry.go:routingAgent`):

- `Machine.ArchiveWorkspace` stats the app subvolume, takes a read-only snapshot
  named `.export-<rand>` under the apps dir (`btrfs.Snapshot(subvol, snap, true,
  "")`), then pipes an archive of the snapshot's `home/app` into the response and
  deletes the snapshot when the stream ends -- including on an early client
  disconnect (the failed write triggers the delete via `pipeArchive`'s `onDone`).
- `Machine.ArchiveSnapshot` resolves the id against the store first (confirming
  it belongs to this app), stats the snapshot subvolume, then pipes an archive of
  its `home/app` with no cleanup -- nothing was created.
- `pipeArchive` runs `archive.Write` on a goroutine writing into an `io.Pipe`,
  returning the read end; the transient snapshot's cleanup hangs off `onDone`.

The archive writer (`archive/archive.go`):

- `archive.Write(root, format, w)` streams `root` as a `.zip` (`Zip`, the
  default) or a gzipped tar (`TarGz`). `Format.Ext()` gives `zip` / `tar.gz`.
- `walk` visits every entry under the files dir with `Lstat` semantics, so a
  **symlink is stored AS a symlink, never followed** -- a link the tenant planted
  cannot redirect the read out of the tree. Sockets, devices and fifos are
  skipped (nothing an archive can carry).

Web UI (`web/src/pages/AppDetail.jsx`): `DownloadMenu` is the icon-plus-two-item
menu used in the header (`base=/api/apps/{app}/export`) and on each snapshot row
(`base=/api/apps/{app}/snapshots/{id}/export`); the `.tar.gz` item just appends
`?format=tar`. `MenuDownloadSub` is the same two choices as an expandable row for
the collapsed kebab menu (header, under "Fork app") and the snapshot row's
overflow menu. All are plain `<a download>` links -- the browser does the fetch
with the session cookie.

## Other notes

- **The live export works on an archived app.** Both endpoints route through the
  node with the plain `route` (not `routeRunnable`), and an archived app's files
  stay readable, so its workspace and its snapshots can still be downloaded --
  which is the point of archiving rather than deleting. See
  [archiving.md](archiving.md).
- **The transient snapshot is dropped as soon as the archive finishes**, and a
  backstop sweep at node startup and every 6h deletes any `.export-*` snapshot
  older than an hour that a crashed export leaked, so a leaked read-only snapshot
  cannot pin the workspace's old blocks. Details in
  [storage-btrfs.md](../subsystems/storage-btrfs.md).
- **A bad snapshot id returns 404, not a path.** The id is validated against the
  store (app-name match) before any filesystem path is built, so a crafted id
  (a `../` traversal, or another app's snapshot) never reaches `filepath.Join`.
  See [security-isolation.md](../subsystems/security-isolation.md).
- Related features: [snapshots-rollback.md](snapshots-rollback.md) (the snapshots
  a per-snapshot download comes from), [browser-workspace.md](browser-workspace.md)
  (the file editor, the other way files move in and out),
  [fork.md](fork.md) (duplicating an app on the server instead of downloading it),
  [rest-api.md](rest-api.md) (the endpoint surface).
