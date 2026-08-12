import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { ErrorBanner, formatDate, formatUsage, Loading, StatusDot } from "../components";

// Empty input means "use the global default"; the API expects null for that.
const numOrNull = (v) => (v === "" ? null : Number(v));
const numOrEmpty = (v) => (v === null || v === undefined ? "" : String(v));

// Built-in assistant spend, formatted compactly for the user table.
const formatUSD = (n) => (!n ? "$0.00" : n < 0.01 ? "<$0.01" : "$" + n.toFixed(2));
const formatTokens = (n) => (n >= 1e6 ? (n / 1e6).toFixed(1) + "M" : n >= 1e3 ? Math.round(n / 1e3) + "k" : String(n || 0));

// One user row with locally edited limit fields; Save appears once dirty.
// AssistantAccess sets, per user, whether External Claude is available and which
// API models they may pick (an empty allowlist means all). A "*" in the summary
// marks an explicit override; without one the user inherits the global default.
const AssistantAccess = ({ user, catalog, externalConfigured, onPatch }) => {
  const [busy, setBusy] = useState(false);
  const models = user.assistant_allowed_models || [];
  const allModels = models.length === 0;
  const patch = async (body) => {
    setBusy(true);
    try {
      await onPatch(user, body);
    } finally {
      setBusy(false);
    }
  };
  const toggleModel = (id) => {
    let set = allModels ? catalog.map((m) => m.id) : models.slice();
    set = set.includes(id) ? set.filter((x) => x !== id) : [...set, id];
    const all = catalog.length > 0 && catalog.every((m) => set.includes(m.id));
    patch({ assistant_allowed_models: all ? [] : set });
  };
  const enabled = allModels ? catalog.length : models.filter((m) => catalog.some((c) => c.id === m)).length;
  const external = user.assistant_external_allowed && externalConfigured;
  return (
    <details className="asst-access">
      <summary title="Assistant access">
        {external ? "External + " : ""}
        {enabled}/{catalog.length} models{user.assistant_has_override ? " *" : ""}
      </summary>
      <div className="asst-access-body">
        <label className={externalConfigured ? "" : "cell-muted"}>
          <input
            type="checkbox"
            checked={user.assistant_external_allowed}
            disabled={busy || !externalConfigured}
            onChange={(e) => patch({ assistant_external_allowed: e.target.checked })}
          />
          External Claude{externalConfigured ? "" : " (not configured)"}
        </label>
        {catalog.map((m) => (
          <label key={m.id}>
            <input type="checkbox" checked={allModels || models.includes(m.id)} disabled={busy} onChange={() => toggleModel(m.id)} />
            {m.label}
          </label>
        ))}
        {user.assistant_has_override && (
          <button type="button" className="btn btn-small" disabled={busy} onClick={() => patch({ assistant_clear_override: true })}>
            Reset to default
          </button>
        )}
      </div>
    </details>
  );
};

const UserRow = ({ user, defaults, catalog, externalConfigured, onPatch, onDelete }) => {
  const [appLimit, setAppLimit] = useState(numOrEmpty(user.app_limit));
  const [memoryMb, setMemoryMb] = useState(numOrEmpty(user.memory_mb));
  const [diskMb, setDiskMb] = useState(numOrEmpty(user.disk_mb));
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setAppLimit(numOrEmpty(user.app_limit));
    setMemoryMb(numOrEmpty(user.memory_mb));
    setDiskMb(numOrEmpty(user.disk_mb));
  }, [user]);

  const dirty =
    appLimit !== numOrEmpty(user.app_limit) || memoryMb !== numOrEmpty(user.memory_mb) || diskMb !== numOrEmpty(user.disk_mb);

  const run = async (body) => {
    setBusy(true);
    try {
      await onPatch(user, body);
    } finally {
      setBusy(false);
    }
  };

  const saveLimits = () =>
    run({
      app_limit: numOrNull(appLimit),
      memory_mb: numOrNull(memoryMb),
      disk_mb: numOrNull(diskMb),
    });

  return (
    <tr>
      <td>
        <div className="item-label">{user.email}</div>
        <div className="cell-muted">{user.name}</div>
      </td>
      <td>
        <span className={`badge${user.role === "admin" ? " badge-accent" : ""}`}>{user.role}</span>
      </td>
      <td>
        <span className={`badge${user.status === "pending" ? " badge-warn" : user.status === "denied" ? " badge-danger" : ""}`}>
          {user.status}
        </span>
      </td>
      <td>{user.app_count}</td>
      <td title="Built-in assistant usage across this user's apps (does not include their own agent)">
        {user.assistant_tokens ? (
          <>
            <div className="item-label">{formatUSD(user.assistant_cost_usd)}</div>
            <div className="cell-muted">{formatTokens(user.assistant_tokens)} tokens</div>
          </>
        ) : (
          <span className="cell-muted">--</span>
        )}
      </td>
      <td>
        <div className="limits-inputs">
          <input
            type="number"
            min="0"
            value={appLimit}
            onChange={(e) => setAppLimit(e.target.value)}
            placeholder={String(defaults.default_app_limit ?? "")}
            aria-label={`App limit for ${user.email}`}
            title="App limit (empty = global default)"
          />
          <input
            type="number"
            min="0"
            value={memoryMb}
            onChange={(e) => setMemoryMb(e.target.value)}
            placeholder={String(defaults.default_memory_mb ?? "")}
            aria-label={`Memory limit (MB) for ${user.email}`}
            title="Memory in MB (empty = global default)"
          />
          <input
            type="number"
            min="0"
            value={diskMb}
            onChange={(e) => setDiskMb(e.target.value)}
            placeholder={String(defaults.default_disk_mb ?? "")}
            aria-label={`Disk limit (MB) for ${user.email}`}
            title="Disk in MB (empty = global default)"
          />
          {dirty && (
            <button type="button" className="btn btn-small btn-primary" onClick={saveLimits} disabled={busy}>
              Save
            </button>
          )}
        </div>
      </td>
      <td>
        <AssistantAccess user={user} catalog={catalog || []} externalConfigured={externalConfigured} onPatch={onPatch} />
      </td>
      <td className="cell-actions">
        <div className="btn-row">
          {user.status !== "active" && (
            <button type="button" className="btn btn-small" onClick={() => run({ status: "active" })} disabled={busy}>
              Approve
            </button>
          )}
          {user.status === "pending" && (
            <button type="button" className="btn btn-small btn-danger" onClick={() => run({ status: "denied" })} disabled={busy}>
              Deny
            </button>
          )}
          <button
            type="button"
            className="btn btn-small"
            onClick={() => run({ role: user.role === "admin" ? "user" : "admin" })}
            disabled={busy}
          >
            {user.role === "admin" ? "Make user" : "Make admin"}
          </button>
          <button type="button" className="btn btn-small btn-danger" onClick={() => onDelete(user)} disabled={busy}>
            Delete
          </button>
        </div>
      </td>
    </tr>
  );
};

// Same shape as the dashboard row, plus the owner: for an admin /api/apps
// returns every user's apps, in server order (already sorted by name).
const AppRow = ({ app }) => (
  <tr>
    <td>
      <StatusDot running={app.running} appRunning={app.app_running} appState={app.app_state} />
      <Link className="mono app-link" to={`/app/${app.name}`}>
        {app.name}
      </Link>
    </td>
    <td className="cell-muted">{app.owner_email || "--"}</td>
    <td>{formatUsage(app.memory_mb, app.memory_limit_mb)}</td>
    <td>{formatUsage(app.disk_mb, app.disk_limit_mb)}</td>
    <td className="cell-muted">{formatDate(app.created_at)}</td>
    <td className="cell-actions">
      <div className="btn-row btn-row-end">
        <Link className="btn btn-small" to={`/app/${app.name}`}>
          Manage
        </Link>
        <a className="btn btn-small btn-primary" href={app.url} target="_blank" rel="noreferrer">
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
      {apps !== null && <span className="usage">{apps.length === 1 ? "1 app" : `${apps.length} apps`}</span>}
    </div>
    {apps === null && !error && <Loading label="Loading apps..." />}
    {apps !== null && apps.length === 0 && <p className="empty">No apps yet.</p>}
    {apps !== null && apps.length > 0 && (
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Owner</th>
              <th>RAM</th>
              <th>Disk</th>
              <th>Created</th>
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
  const [choice, setChoice] = useState(user.app_count > 0 ? "transfer" : "delete");
  const [target, setTarget] = useState(others.length > 0 ? others[0].id : "");
  const [busy, setBusy] = useState(false);
  const canTransfer = others.length > 0;

  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const query = choice === "transfer" ? `?apps=transfer&transfer_to=${target}` : "?apps=delete";
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
        {user.app_count === 0 && <p>This account has no apps. Deleting it removes the account, its keys and its tokens.</p>}
        {user.app_count > 0 && (
          <>
            <p>
              This account owns {user.app_count} {user.app_count === 1 ? "app" : "apps"}. What should happen to them?
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
                  <select value={target} onChange={(e) => setTarget(e.target.value)} disabled={choice !== "transfer"}>
                    {others.map((u) => (
                      <option key={u.id} value={u.id}>
                        {u.email}
                      </option>
                    ))}
                  </select>
                ) : (
                  <span className="cell-muted"> -- no other active account to give them to</span>
                )}
              </span>
            </label>
            <label className="choice">
              <input type="radio" name="apps" value="delete" checked={choice === "delete"} onChange={() => setChoice("delete")} />
              <span>
                Delete the apps too -- their containers, files and URLs go with them. <strong>This cannot be undone.</strong>
              </span>
            </label>
          </>
        )}
        <div className="btn-row">
          <button type="button" className="btn" onClick={onCancel} disabled={busy}>
            Cancel
          </button>
          <button type="submit" className="btn btn-danger" disabled={busy || (choice === "transfer" && !target)}>
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
      <select value={role} onChange={(e) => setRole(e.target.value)} aria-label="Role">
        <option value="user">User</option>
        <option value="admin">Admin</option>
      </select>
      <button type="submit" className="btn btn-primary" disabled={busy || email.trim() === ""}>
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
    if (!window.confirm(`Stop auto-approving @${d.domain}? Users already approved keep their accounts.`)) {
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
        Anyone signing in with a Google address in one of these domains is approved automatically. Everyone else waits in the
        list above. Write it as <span className="mono">company.com</span> or <span className="mono">*@company.com</span>.
        Public providers (gmail.com, outlook.com, ...) are refused: allowing one would let anyone in.
      </p>
      {domains === null && <Loading label="Loading domains..." />}
      {domains !== null && domains.length === 0 && <p className="empty">No domains yet: every sign-up needs approval.</p>}
      {domains !== null && domains.length > 0 && (
        <ul className="item-list">
          {domains.map((d) => (
            <li key={d.domain}>
              <div>
                <div className="item-label mono">*@{d.domain}</div>
                <div className="cell-muted">added {formatDate(d.created_at)}</div>
              </div>
              <button type="button" className="btn btn-small btn-danger" onClick={() => remove(d)}>
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
        <button type="submit" className="btn btn-primary" disabled={busy || domain.trim() === ""}>
          {busy ? "Adding..." : "Allow domain"}
        </button>
      </form>
    </div>
  );
};

const Defaults = ({ settings, asstDefaults, onSaved, setError }) => {
  const [appLimit, setAppLimit] = useState(numOrEmpty(settings.default_app_limit));
  const [memoryMb, setMemoryMb] = useState(numOrEmpty(settings.default_memory_mb));
  const [diskMb, setDiskMb] = useState(numOrEmpty(settings.default_disk_mb));
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
      <p className="hint">Applied to users without a per-user override.</p>
      <form className="defaults-form" onSubmit={save}>
        <label>
          App limit
          <input type="number" min="0" required value={appLimit} onChange={(e) => setAppLimit(e.target.value)} />
        </label>
        <label>
          Memory (MB)
          <input type="number" min="0" required value={memoryMb} onChange={(e) => setMemoryMb(e.target.value)} />
        </label>
        <label>
          Disk (MB)
          <input type="number" min="0" required value={diskMb} onChange={(e) => setDiskMb(e.target.value)} />
        </label>
        <button type="submit" className="btn btn-primary" disabled={busy}>
          {busy ? "Saving..." : saved ? "Saved!" : "Save"}
        </button>
      </form>
      {asstDefaults && <AssistantDefaults defaults={asstDefaults} onSaved={onSaved} setError={setError} />}
    </div>
  );
};

const AdminInner = () => {
  const [users, setUsers] = useState(null);
  const [apps, setApps] = useState(null);
  const [settings, setSettings] = useState(null);
  const [asstDefaults, setAsstDefaults] = useState(null);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const [u, a, s, ad] = await Promise.all([
        api.get("/api/users"),
        api.get("/api/apps?all=true"),
        api.get("/api/settings"),
        api.get("/api/assistant-defaults"),
      ]);
      setUsers(u);
      setApps(a);
      setSettings(s);
      setAsstDefaults(ad);
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
      <div className="card">
        <h2>Users</h2>
        {(users === null || settings === null) && !error && <Loading label="Loading users..." />}
        {users !== null && settings !== null && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>User</th>
                  <th>Role</th>
                  <th>Status</th>
                  <th>Apps</th>
                  <th>Assistant</th>
                  <th>Agent access</th>
                  <th>Limits (apps / mem MB / disk MB)</th>
                  <th aria-label="Actions" />
                </tr>
              </thead>
              <tbody>
                {users.map((user) => (
                  <UserRow
                    key={user.id}
                    user={user}
                    defaults={settings}
                    catalog={asstDefaults?.models || []}
                    externalConfigured={!!asstDefaults?.external_configured}
                    onPatch={patchUser}
                    onDelete={setDeleting}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
        <InviteUser onAdded={load} setError={setError} />
        <p className="hint">Adding someone here approves them up front; they still sign in with Google.</p>
      </div>
      <AllowedDomains setError={setError} />
      <AllApps apps={apps} error={error} />
      {settings !== null && <Defaults settings={settings} asstDefaults={asstDefaults} onSaved={load} setError={setError} />}
    </>
  );
};

// AssistantDefaults edits the global defaults new users inherit: whether External
// Claude is allowed, the default agent, and which models are available. Changing a
// default re-fetches the users so their inherited access updates too.
const AssistantDefaults = ({ defaults, onSaved, setError }) => {
  const [busy, setBusy] = useState(false);
  const models = defaults.allowed_models || [];
  const allModels = models.length === 0;
  const catalog = defaults.models || [];
  const save = async (body) => {
    setBusy(true);
    setError("");
    try {
      await api.put("/api/assistant-defaults", body);
      await onSaved();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };
  const toggleModel = (id) => {
    let set = allModels ? catalog.map((m) => m.id) : models.slice();
    set = set.includes(id) ? set.filter((x) => x !== id) : [...set, id];
    const all = catalog.length > 0 && catalog.every((m) => set.includes(m.id));
    save({ allowed_models: all ? [] : set });
  };
  const modeOptions = [
    ...(defaults.external_configured ? [{ id: "external-claude", label: "Claude.ai" }] : []),
    ...catalog,
  ];
  return (
    <div className="defaults-assistant">
      <h3>Assistant</h3>
      <p className="hint">Which agent new conversations use, and what each user may pick (unless overridden per user).</p>
      <div className="asst-defaults">
        <label>
          <input
            type="checkbox"
            checked={defaults.external_allowed}
            disabled={busy || !defaults.external_configured}
            onChange={(e) => save({ external_allowed: e.target.checked })}
          />
          Allow External Claude{defaults.external_configured ? "" : " (subscription not configured)"}
        </label>
        <label className="asst-defaults-mode">
          Default agent
          <select value={defaults.default_mode} disabled={busy} onChange={(e) => save({ default_mode: e.target.value })}>
            {modeOptions.map((m) => (
              <option key={m.id} value={m.id}>
                {m.label}
              </option>
            ))}
          </select>
        </label>
        <div className="asst-defaults-models">
          <span className="hint">Models available by default</span>
          {catalog.map((m) => (
            <label key={m.id}>
              <input type="checkbox" checked={allModels || models.includes(m.id)} disabled={busy} onChange={() => toggleModel(m.id)} />
              {m.label}
            </label>
          ))}
        </div>
      </div>
    </div>
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
