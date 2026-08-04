import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import { ErrorBanner, Loading } from "../components";

// Empty input means "use the global default"; the API expects null for that.
const numOrNull = (v) => (v === "" ? null : Number(v));
const numOrEmpty = (v) => (v === null || v === undefined ? "" : String(v));

// One user row with locally edited limit fields; Save appears once dirty.
const UserRow = ({ user, defaults, onPatch, onDelete }) => {
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

const Defaults = ({ settings, onSaved, setError }) => {
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
      await api.patch("/v1/settings", {
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
      <p className="hint">Limits applied to users without a per-user override.</p>
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
    </div>
  );
};

const AdminInner = () => {
  const [users, setUsers] = useState(null);
  const [settings, setSettings] = useState(null);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const [u, s] = await Promise.all([api.get("/v1/users"), api.get("/v1/settings")]);
      setUsers(u);
      setSettings(s);
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
      await api.patch(`/v1/users/${user.id}`, body);
      await load();
    } catch (err) {
      setError(err.message);
    }
  };

  const deleteUser = async (user) => {
    if (
      !window.confirm(
        `Delete user ${user.email}? This permanently deletes the user and all of their apps (${user.app_count}).`,
      )
    ) {
      return;
    }
    setError("");
    try {
      await api.del(`/v1/users/${user.id}`);
      await load();
    } catch (err) {
      setError(err.message);
    }
  };

  return (
    <>
      <div className="page-header">
        <h1>Admin</h1>
      </div>
      <ErrorBanner message={error} onDismiss={() => setError("")} />
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
                  <th>Limits (apps / mem MB / disk MB)</th>
                  <th aria-label="Actions" />
                </tr>
              </thead>
              <tbody>
                {users.map((user) => (
                  <UserRow key={user.id} user={user} defaults={settings} onPatch={patchUser} onDelete={deleteUser} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      {settings !== null && <Defaults settings={settings} onSaved={load} setError={setError} />}
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
