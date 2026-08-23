import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import {
  ErrorBanner,
  formatDate,
  Loading,
  StatusDot,
  UsagePair, pairMB, Skeleton } from "../components";

// Empty input means "use the global default"; the API expects null for that.
const numOrNull = (v) => (v === "" ? null : Number(v));
const numOrEmpty = (v) => (v === null || v === undefined ? "" : String(v));

// Built-in assistant spend, formatted compactly for the user table.
const formatUSD = (n) =>
  !n ? "$0.00" : n < 0.01 ? "<$0.01" : "$" + n.toFixed(2);
const formatTokens = (n) =>
  n >= 1e6
    ? (n / 1e6).toFixed(1) + "M"
    : n >= 1e3
      ? Math.round(n / 1e3) + "k"
      : String(n || 0);

// Kebab is a row-overflow menu that escapes the table's scroll container: the
// popup is position:fixed at the button's measured corner, and any outside
// press, scroll or resize closes it (a fixed popup must not drift from a
// scrolled-away button).
const Kebab = ({ label, children }) => {
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState({ top: 0, right: 0 });
  const btnRef = useRef(null);
  useEffect(() => {
    if (!open) return undefined;
    const close = () => setOpen(false);
    const onDown = (e) => {
      if (!btnRef.current?.contains(e.target) && !e.target.closest(".kebab-menu")) close();
    };
    const onEsc = (e) => e.key === "Escape" && close();
    window.addEventListener("scroll", close, true);
    window.addEventListener("resize", close);
    document.addEventListener("pointerdown", onDown);
    document.addEventListener("keydown", onEsc);
    return () => {
      window.removeEventListener("scroll", close, true);
      window.removeEventListener("resize", close);
      document.removeEventListener("pointerdown", onDown);
      document.removeEventListener("keydown", onEsc);
    };
  }, [open]);
  const toggle = () => {
    const r = btnRef.current.getBoundingClientRect();
    setPos({ top: r.bottom + 4, right: window.innerWidth - r.right });
    setOpen(!open);
  };
  return (
    <>
      <button ref={btnRef} type="button" className="btn btn-small" onClick={toggle} aria-label={label} aria-expanded={open} title="More actions">
        &#8942;
      </button>
      {open && (
        <div className="kebab-menu" style={{ top: pos.top, right: pos.right }} onClick={() => setOpen(false)}>
          {children}
        </div>
      )}
    </>
  );
};

// EditUserDialog is where a user's limits live now: app count and the two
// pools, with the instance defaults as placeholders. The list shows, this edits.
const EditUserDialog = ({ user, defaults, onCancel, onSave }) => {
  const [appLimit, setAppLimit] = useState(numOrEmpty(user.app_limit));
  const [memoryPoolMb, setMemoryPoolMb] = useState(numOrEmpty(user.memory_pool_mb));
  const [diskPoolMb, setDiskPoolMb] = useState(numOrEmpty(user.disk_pool_mb));
  const [busy, setBusy] = useState(false);
  const save = async (e) => {
    e.preventDefault();
    setBusy(true);
    try {
      await onSave({
        app_limit: numOrNull(appLimit),
        memory_pool_mb: numOrNull(memoryPoolMb),
        disk_pool_mb: numOrNull(diskPoolMb),
      });
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onCancel}>
      <form className="card modal modal-sheet" onMouseDown={(e) => e.stopPropagation()} onSubmit={save}>
        <h2>Limits for {user.email}</h2>
        <p className="hint">
          Empty fields fall back to the instance defaults. The pools bound what all of this
          user&apos;s apps together may allocate -- independent of how big one new app starts.
        </p>
        <label className="settings-field">
          <span>App limit</span>
          <input type="number" min="0" className="settings-input" value={appLimit} onChange={(e) => setAppLimit(e.target.value)} placeholder={`${defaults.default_app_limit ?? ""} (default)`} disabled={busy} />
        </label>
        <label className="settings-field">
          <span>RAM pool (MB)</span>
          <input type="number" min="0" className="settings-input" value={memoryPoolMb} onChange={(e) => setMemoryPoolMb(e.target.value)} placeholder={`${defaults.default_memory_pool_mb ?? ""} (default)`} disabled={busy} />
        </label>
        <label className="settings-field">
          <span>Disk pool (MB)</span>
          <input type="number" min="0" className="settings-input" value={diskPoolMb} onChange={(e) => setDiskPoolMb(e.target.value)} placeholder={`${defaults.default_disk_pool_mb ?? ""} (default)`} disabled={busy} />
        </label>
        <div className="btn-row">
          <button type="button" className="btn" onClick={onCancel} disabled={busy}>
            Cancel
          </button>
          <button type="submit" className="btn btn-primary" disabled={busy}>
            {busy ? "Saving..." : "Save"}
          </button>
        </div>
      </form>
    </div>
  );
};

// One user row with locally edited limit fields; Save appears once dirty.
const UserRow = ({ user, defaults, onPatch, onDelete }) => {
  const [busy, setBusy] = useState(false);
  const [confirmAdmin, setConfirmAdmin] = useState(false);
  const [editing, setEditing] = useState(false);

  // What the row DISPLAYS; editing happens in the dialog. An inherited value
  // is shown muted, so an explicit setting reads differently from a fallback.
  const effectiveAppLimit = user.app_limit ?? defaults.default_app_limit ?? 0;

  const run = async (body) => {
    setBusy(true);
    try {
      await onPatch(user, body);
    } finally {
      setBusy(false);
    }
  };



  return (
    <tr>
      <td>
        <div className="item-label">{user.email}</div>
        <div className="cell-muted">{user.name}</div>
      </td>
      <td>
        <span
          className={`badge${user.role === "admin" ? " badge-accent" : ""}`}
        >
          {user.role}
        </span>
      </td>
      <td>
        <span
          className={`badge${user.status === "pending" ? " badge-warn" : user.status === "denied" ? " badge-danger" : ""}`}
        >
          {user.status}
        </span>
      </td>
      <td>{user.app_count}</td>
      <td title="Built-in assistant usage across this user's apps (does not include their own agent)">
        {user.assistant_tokens ? (
          <>
            <div className="item-label">
              {formatUSD(user.assistant_cost_usd)}
            </div>
            <div className="cell-muted">
              {formatTokens(user.assistant_tokens)} tokens
            </div>
          </>
        ) : (
          <span className="cell-muted">--</span>
        )}
      </td>
      <td title="How many apps this user may create">
        {user.app_limit ?? <span className="cell-muted">{effectiveAppLimit}</span>}
      </td>
      <td title="The RAM budget all their apps' limits share">
        {user.memory_pool_mb != null ? pairMB(user.memory_pool_mb, 0) : <span className="cell-muted">{pairMB(defaults.default_memory_pool_mb || 0, 0)}</span>}
      </td>
      <td title="The disk budget all their apps' limits share">
        {user.disk_pool_mb != null ? pairMB(user.disk_pool_mb, 0) : <span className="cell-muted">{pairMB(defaults.default_disk_pool_mb || 0, 0)}</span>}
      </td>
      <td className="cell-actions">
        <div className="btn-row">
          {user.status !== "active" && (
            <button
              type="button"
              className="btn btn-small"
              onClick={() => run({ status: "active" })}
              disabled={busy}
            >
              Approve
            </button>
          )}
          {user.status === "pending" && (
            <button
              type="button"
              className="btn btn-small btn-danger"
              onClick={() => run({ status: "denied" })}
              disabled={busy}
            >
              Deny
            </button>
          )}
          <Kebab label={`More actions for ${user.email}`}>
            <button type="button" onClick={() => setEditing(true)} disabled={busy}>
              Edit limits...
            </button>
            {user.role === "admin" ? (
              <button type="button" onClick={() => run({ role: "user" })} disabled={busy}>
                Make user
              </button>
            ) : (
              <button type="button" onClick={() => setConfirmAdmin(true)} disabled={busy}>
                Make admin
              </button>
            )}
            <button type="button" className="kebab-danger" onClick={() => onDelete(user)} disabled={busy}>
              Delete user
            </button>
          </Kebab>
        </div>
      </td>
      {editing && (
        <EditUserDialog
          user={user}
          defaults={defaults}
          onCancel={() => setEditing(false)}
          onSave={async (body) => {
            await onPatch(user, body);
            setEditing(false);
          }}
        />
      )}
      {confirmAdmin && (
        <MakeAdminDialog
          user={user}
          onCancel={() => setConfirmAdmin(false)}
          onConfirm={() => {
            setConfirmAdmin(false);
            run({ role: "admin" });
          }}
        />
      )}
    </tr>
  );
};

// MakeAdminDialog spells out what the promotion means before it happens: an
// admin manages every user, app, pool and global setting on the instance.
const MakeAdminDialog = ({ user, onCancel, onConfirm }) => (
  <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onCancel}>
    <div className="card modal modal-sheet" onMouseDown={(e) => e.stopPropagation()}>
      <h2>Make {user.email} an admin?</h2>
      <p className="hint">
        Admins manage <b>everything</b> on this instance: all users and their apps, resource pools,
        approvals, and the global settings. They can also make and unmake other admins.
      </p>
      <div className="btn-row">
        <button type="button" className="btn" onClick={onCancel}>
          Cancel
        </button>
        <button type="button" className="btn btn-primary" onClick={onConfirm}>
          Make admin
        </button>
      </div>
    </div>
  </div>
);

// Same shape as the dashboard row, plus the owner: for an admin /api/apps
// returns every user's apps, in server order (already sorted by name).
const AppRow = ({ app }) => (
  <tr>
    <td>
      <StatusDot
        running={app.running}
        appRunning={app.app_running}
        appState={app.app_state}
        archived={app.archived}
      />
      <Link className="mono app-link" to={`/app/${app.name}`}>
        {app.name}
      </Link>
    </td>
    <td className="cell-muted">{app.owner_email || "--"}</td>
    <td><UsagePair kind="ram" used={app.memory_mb} total={app.memory_limit_mb} /></td>
    <td><UsagePair kind="disk" used={app.disk_mb} total={app.disk_limit_mb} /></td>
    <td className="mono cell-muted">{app.host || "--"}</td>
    <td className="cell-actions">
      <div className="btn-row btn-row-end">
        <Link className="btn btn-small" to={`/app/${app.name}`}>
          Manage
        </Link>
        <a
          className="btn btn-small btn-primary"
          href={app.url}
          target="_blank"
          rel="noreferrer"
        >
          Open app
        </a>
      </div>
    </td>
  </tr>
);

const AllApps = ({ apps, error }) => (
  <div className="card">
    <div className="card-header">
      <h2>All apps</h2>
      {apps !== null && (
        <span className="usage">
          {apps.length === 1 ? "1 app" : `${apps.length} apps`}
        </span>
      )}
    </div>
    {apps === null && !error && <Skeleton rows={4} label="Loading apps..." />}
    {apps !== null && apps.length === 0 && (
      <p className="empty">No apps yet.</p>
    )}
    {apps !== null && apps.length > 0 && (
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Owner</th>
              <th>RAM</th>
              <th>Disk</th>
              <th>Node</th>
              <th aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {apps.map((app) => (
              <AppRow key={app.name} app={app} />
            ))}
          </tbody>
        </table>
      </div>
    )}
  </div>
);

// Deleting a person raises a question about their apps that only an admin can
// answer, so it is asked rather than assumed. An ownerless app would keep
// serving with nobody able to manage it, which is why that is not an option.
const DeleteUserDialog = ({ user, users, onCancel, onDone, setError }) => {
  const others = users.filter((u) => u.id !== user.id && u.status === "active");
  const [choice, setChoice] = useState(
    user.app_count > 0 ? "transfer" : "delete",
  );
  const [target, setTarget] = useState(others.length > 0 ? others[0].id : "");
  const [busy, setBusy] = useState(false);
  const canTransfer = others.length > 0;

  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const query =
        choice === "transfer"
          ? `?apps=transfer&transfer_to=${target}`
          : "?apps=delete";
      await api.del(`/api/users/${user.id}${query}`);
      onDone();
    } catch (err) {
      setError(err.message);
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true">
      <form className="card modal" onSubmit={submit}>
        <h2>Delete {user.email}</h2>
        {user.app_count === 0 && (
          <p>
            This account has no apps. Deleting it removes the account, its keys
            and its tokens.
          </p>
        )}
        {user.app_count > 0 && (
          <>
            <p>
              This account owns {user.app_count}{" "}
              {user.app_count === 1 ? "app" : "apps"}. What should happen to
              them?
            </p>
            <label className="choice">
              <input
                type="radio"
                name="apps"
                value="transfer"
                checked={choice === "transfer"}
                disabled={!canTransfer}
                onChange={() => setChoice("transfer")}
              />
              <span>
                Give them to another user
                {canTransfer ? (
                  <select
                    value={target}
                    onChange={(e) => setTarget(e.target.value)}
                    disabled={choice !== "transfer"}
                  >
                    {others.map((u) => (
                      <option key={u.id} value={u.id}>
                        {u.email}
                      </option>
                    ))}
                  </select>
                ) : (
                  <span className="cell-muted">
                    {" "}
                    -- no other active account to give them to
                  </span>
                )}
              </span>
            </label>
            <label className="choice">
              <input
                type="radio"
                name="apps"
                value="delete"
                checked={choice === "delete"}
                onChange={() => setChoice("delete")}
              />
              <span>
                Delete the apps too -- their containers, files and URLs go with
                them. <strong>This cannot be undone.</strong>
              </span>
            </label>
          </>
        )}
        <div className="btn-row">
          <button
            type="button"
            className="btn"
            onClick={onCancel}
            disabled={busy}
          >
            Cancel
          </button>
          <button
            type="submit"
            className="btn btn-danger"
            disabled={busy || (choice === "transfer" && !target)}
          >
            {busy ? "Deleting..." : "Delete user"}
          </button>
        </div>
      </form>
    </div>
  );
};

// Adding a user here creates an approved account before they have ever signed
// in: their first Google login lands straight on the dashboard.
const InviteUser = ({ onAdded, setError }) => {
  const [email, setEmail] = useState("");
  const [role, setRole] = useState("user");
  const [busy, setBusy] = useState(false);

  const add = async (e) => {
    e.preventDefault();
    if (busy || email.trim() === "") {
      return;
    }
    setBusy(true);
    setError("");
    try {
      await api.post("/api/users", { email: email.trim(), role });
      setEmail("");
      setRole("user");
      onAdded();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="inline-form" onSubmit={add}>
      <input
        type="email"
        value={email}
        onChange={(e) => setEmail(e.target.value)}
        placeholder="person@company.com"
        aria-label="Email address to add"
      />
      <select
        value={role}
        onChange={(e) => setRole(e.target.value)}
        aria-label="Role"
      >
        <option value="user">User</option>
        <option value="admin">Admin</option>
      </select>
      <button
        type="submit"
        className="btn btn-primary"
        disabled={busy || email.trim() === ""}
      >
        {busy ? "Adding..." : "Add user"}
      </button>
    </form>
  );
};

// Anyone whose Google address ends in one of these domains is approved on sign
// in, so a whole company can onboard itself.
const AllowedDomains = ({ setError }) => {
  const [domains, setDomains] = useState(null);
  const [domain, setDomain] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setDomains(await api.get("/api/domains"));
    } catch (err) {
      setError(err.message);
    }
  }, [setError]);

  useEffect(() => {
    load();
  }, [load]);

  const add = async (e) => {
    e.preventDefault();
    if (busy || domain.trim() === "") {
      return;
    }
    setBusy(true);
    setError("");
    try {
      await api.post("/api/domains", { domain: domain.trim() });
      setDomain("");
      await load();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (d) => {
    if (
      !window.confirm(
        `Stop auto-approving @${d.domain}? Users already approved keep their accounts.`,
      )
    ) {
      return;
    }
    setError("");
    try {
      await api.del(`/api/domains/${encodeURIComponent(d.domain)}`);
      await load();
    } catch (err) {
      setError(err.message);
    }
  };

  return (
    <div className="card">
      <h2>Sign-up without approval</h2>
      <p className="hint">
        Anyone signing in with a Google address in one of these domains is
        approved automatically. Everyone else waits in the list above. Write it
        as <span className="mono">company.com</span> or{" "}
        <span className="mono">*@company.com</span>. Public providers
        (gmail.com, outlook.com, ...) are refused: allowing one would let anyone
        in.
      </p>
      {domains === null && <Skeleton rows={2} label="Loading domains..." />}
      {domains !== null && domains.length === 0 && (
        <p className="empty">No domains yet: every sign-up needs approval.</p>
      )}
      {domains !== null && domains.length > 0 && (
        <ul className="item-list">
          {domains.map((d) => (
            <li key={d.domain}>
              <div>
                <div className="item-label mono">*@{d.domain}</div>
                <div className="cell-muted">
                  added {formatDate(d.created_at)}
                </div>
              </div>
              <button
                type="button"
                className="btn btn-small btn-danger"
                onClick={() => remove(d)}
              >
                Remove
              </button>
            </li>
          ))}
        </ul>
      )}
      <form className="inline-form" onSubmit={add}>
        <input
          type="text"
          value={domain}
          onChange={(e) => setDomain(e.target.value)}
          placeholder="company.com"
          aria-label="Email domain to allow"
        />
        <button
          type="submit"
          className="btn btn-primary"
          disabled={busy || domain.trim() === ""}
        >
          {busy ? "Adding..." : "Allow domain"}
        </button>
      </form>
    </div>
  );
};

const Defaults = ({ settings, onSaved, setError }) => {
  const [appLimit, setAppLimit] = useState(
    numOrEmpty(settings.default_app_limit),
  );
  const [memoryMb, setMemoryMb] = useState(
    numOrEmpty(settings.default_memory_mb),
  );
  const [diskMb, setDiskMb] = useState(numOrEmpty(settings.default_disk_mb));
  const [memoryPoolMb, setMemoryPoolMb] = useState(numOrEmpty(settings.default_memory_pool_mb));
  const [diskPoolMb, setDiskPoolMb] = useState(numOrEmpty(settings.default_disk_pool_mb));
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);

  const save = async (e) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    setSaved(false);
    try {
      await api.patch("/api/settings", {
        default_app_limit: Number(appLimit),
        default_memory_mb: Number(memoryMb),
        default_disk_mb: Number(diskMb),
        default_memory_pool_mb: Number(memoryPoolMb),
        default_disk_pool_mb: Number(diskPoolMb),
      });
      setSaved(true);
      onSaved();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card">
      <h2>Global defaults</h2>
      <p className="hint">
        What a new user and a new app start with. The two <b>per app</b> rows size one app; the two{" "}
        <b>pool</b> rows are the total a user&apos;s apps may allocate together -- independent numbers,
        not one derived from the other. Any user&apos;s row in the Users list overrides these.
      </p>
      <form className="defaults-rows" onSubmit={save}>
        <label className="settings-field">
          <span>Apps per user</span>
          <input type="number" min="0" required className="settings-input" value={appLimit} onChange={(e) => setAppLimit(e.target.value)} />
        </label>
        <label className="settings-field">
          <span>RAM per new app (MB)</span>
          <input type="number" min="0" required className="settings-input" value={memoryMb} onChange={(e) => setMemoryMb(e.target.value)} />
        </label>
        <label className="settings-field">
          <span>Disk per new app (MB)</span>
          <input type="number" min="0" required className="settings-input" value={diskMb} onChange={(e) => setDiskMb(e.target.value)} />
        </label>
        <label className="settings-field">
          <span>RAM pool per user (MB)</span>
          <input type="number" min="1" required className="settings-input" value={memoryPoolMb} onChange={(e) => setMemoryPoolMb(e.target.value)} />
        </label>
        <label className="settings-field">
          <span>Disk pool per user (MB)</span>
          <input type="number" min="1" required className="settings-input" value={diskPoolMb} onChange={(e) => setDiskPoolMb(e.target.value)} />
        </label>
        <div className="btn-row" style={{ justifyContent: "flex-start" }}>
          <button type="submit" className="btn btn-primary" disabled={busy}>
            {busy ? "Saving..." : saved ? "Saved!" : "Save"}
          </button>
        </div>
      </form>
    </div>
  );
};

// The cluster: which machines are in it, whether they are reporting, and what
// they are carrying. Everything here is what the daemon last recorded, which is
// why a member's row leads with how long ago it reported rather than a green
// light that would imply a live check.
const relativeAge = (iso) => {
  if (!iso) return null;
  const seconds = Math.max(
    0,
    Math.round((Date.now() - new Date(iso).getTime()) / 1000),
  );
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.round(seconds / 3600)}h ago`;
  return `${Math.round(seconds / 86400)}d ago`;
};

const formatMB = (mb) =>
  mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${mb || 0} MB`;

// The build string carries commit and timestamp; the row wants the version.
const shortVersion = (v) => (v ? v.split(" ")[0] : null);

const MemberRow = ({ member, kind }) => {
  const stats = member.stats || {};
  const carrying =
    kind === "node"
      ? `${member.apps} ${member.apps === 1 ? "app" : "apps"}`
      : kind === "proxy"
        ? `${member.routes || 0} routes`
        : "registry";
  const detail =
    kind === "node" ? member.address : shortVersion(member.version);
  return (
    <tr>
      <td>
        <span
          className={`status-dot ${member.stale ? "status-down" : "status-up"}`}
          title={member.stale ? "Has not reported recently" : "Reporting"}
        />
        <span className="mono">{member.name}</span>
        {detail && <div className="cell-muted mono member-detail">{detail}</div>}
      </td>
      <td>
        <span className="badge">{kind}</span>
      </td>
      <td className="cell-muted">{carrying}</td>
      <td>
        {stats.memory_total_mb ? (
          <UsagePair kind="ram" used={stats.memory_used_mb} total={stats.memory_total_mb} />
        ) : (
          <span className="cell-muted">--</span>
        )}
      </td>
      <td>
        {stats.disk_total_mb ? (
          <UsagePair kind="disk" used={stats.disk_used_mb} total={stats.disk_total_mb} />
        ) : (
          <span className="cell-muted">--</span>
        )}
      </td>
      <td
        className={loadLevel(stats)}
        title={stats.cpu_count ? `1-minute load average across ${stats.cpu_count} core(s)` : ""}
      >
        {stats.cpu_count ? `${(stats.load1 || 0).toFixed(2)} / ${stats.cpu_count}` : <span className="cell-muted">--</span>}
      </td>
      <td className={member.stale ? "cell-warn" : "cell-muted"}>
        {member.last_seen ? relativeAge(member.last_seen) : "never reported"}
      </td>
    </tr>
  );
};

// A box is busy when load approaches its core count and overloaded past it --
// the same warn/crit language the memory and disk pairs use.
const loadLevel = (stats) => {
  if (!stats.cpu_count) return "";
  const ratio = (stats.load1 || 0) / stats.cpu_count;
  return ratio >= 1 ? "usage-crit" : ratio >= 0.75 ? "usage-warn" : "";
};

// Every member in one table -- control included, since its own box filling up
// is what stops the registry, and it is the one member that never dials in.
const MemberTable = ({ members }) => (
  <div className="table-wrap">
    <table>
      <thead>
        <tr>
          <th>Member</th>
          <th>Role</th>
          <th>Carrying</th>
          <th>RAM</th>
          <th>Disk</th>
          <th>Load / CPUs</th>
          <th>Reported</th>
        </tr>
      </thead>
      <tbody>
        {members.map((m) => (
          <MemberRow key={m.kind + m.name} member={m} kind={m.kind} />
        ))}
      </tbody>
    </table>
  </div>
);

const Cluster = ({ status, error }) => {
  if (status === null)
    return (
      <div className="card">
        {!error && <Skeleton rows={3} label="Loading cluster..." />}
      </div>
    );
  const quiet = [...(status.nodes || []), ...(status.proxies || [])].filter(
    (m) => m.stale,
  ).length;
  return (
    <div className="card">
      <div className="card-header">
        <h2>Cluster</h2>
        {quiet > 0 && (
          <span className="usage usage-warn">
            {quiet} {quiet === 1 ? "member has" : "members have"} not reported
            recently
          </span>
        )}
      </div>
      <MemberTable
        members={[
          ...(status.control ? [{ ...status.control, kind: "control" }] : []),
          ...(status.nodes || []).map((n) => ({ ...n, kind: "node" })),
          ...(status.proxies || []).map((p) => ({ ...p, kind: "proxy" })),
        ]}
      />
      <div className="cluster-stats">
        <div className="cluster-stat">
          <span className="k">Apps</span>
          <span className="v">{status.apps.total}</span>
        </div>
        <div className="cluster-stat">
          <span className="k">Powered off</span>
          <span className="v">{status.apps.powered_off}</span>
        </div>
        <div className="cluster-stat">
          <span className="k">Snapshots</span>
          <span className="v">{status.apps.snapshots}</span>
        </div>
        <div className="cluster-stat">
          <span className="k">Disk used</span>
          <span className="v">{formatMB(status.apps.disk_used_mb)}</span>
        </div>
        <div className="cluster-stat">
          <span className="k">People</span>
          <span className="v">
            {status.people.total}
            {status.people.pending > 0
              ? ` (${status.people.pending} pending)`
              : ""}
          </span>
        </div>
      </div>
      {status.apps.unplaced > 0 && (
        <p className="hint hint-warn">
          {status.apps.unplaced}{" "}
          {status.apps.unplaced === 1 ? "app is" : "apps are"} on a node that is
          not registered, so they are not routable. Re-register the node, or
          move the apps.
        </p>
      )}
    </div>
  );
};

const AdminInner = () => {
  const [users, setUsers] = useState(null);
  const [apps, setApps] = useState(null);
  const [settings, setSettings] = useState(null);
  const [cluster, setCluster] = useState(null);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const [u, a, s, c] = await Promise.all([
        api.get("/api/users"),
        api.get("/api/apps?all=true"),
        api.get("/api/settings"),
        api.get("/api/cluster"),
      ]);
      setUsers(u);
      setApps(a);
      setSettings(s);
      setCluster(c);
    } catch (err) {
      setError(err.message);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const patchUser = async (user, body) => {
    setError("");
    try {
      await api.patch(`/api/users/${user.id}`, body);
      await load();
    } catch (err) {
      setError(err.message);
    }
  };

  const [deleting, setDeleting] = useState(null);

  return (
    <>
      <div className="page-header">
        <h1>Admin</h1>
      </div>
      <ErrorBanner message={error} onDismiss={() => setError("")} />
      {deleting && (
        <DeleteUserDialog
          user={deleting}
          users={users || []}
          setError={setError}
          onCancel={() => setDeleting(null)}
          onDone={() => {
            setDeleting(null);
            load();
          }}
        />
      )}
      <Cluster status={cluster} error={error} />
      <div className="card">
        <h2>Users</h2>
        {(users === null || settings === null) && !error && (
          <Skeleton rows={4} label="Loading users..." />
        )}
        {users !== null && settings !== null && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>User</th>
                  <th>Role</th>
                  <th>Status</th>
                  <th>Apps</th>
                  <th>Agent access</th>
                  <th>App limit</th>
                  <th>Max RAM</th>
                  <th>Max Disk</th>
                  <th aria-label="Actions" />
                </tr>
              </thead>
              <tbody>
                {users.map((user) => (
                  <UserRow
                    key={user.id}
                    user={user}
                    defaults={settings}
                    onPatch={patchUser}
                    onDelete={setDeleting}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
        <InviteUser onAdded={load} setError={setError} />
        <p className="hint">
          Adding someone here approves them up front; they still sign in with
          Google.
        </p>
      </div>
      <AllowedDomains setError={setError} />
      <AllApps apps={apps} error={error} />
      {settings !== null && (
        <Defaults settings={settings} onSaved={load} setError={setError} />
      )}
    </>
  );
};

const Admin = ({ account }) => {
  if (account.role !== "admin") {
    return (
      <div className="card">
        <h2>Not authorized</h2>
        <p>This page is only available to admins.</p>
      </div>
    );
  }
  return <AdminInner />;
};

export default Admin;
