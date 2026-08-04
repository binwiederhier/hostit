import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api } from "../api";
import { ErrorBanner, Loading } from "../components";

const nameRe = /^[a-z][a-z0-9-]{0,30}[a-z0-9]$/;

const AppRow = ({ app, account, isAdmin, onDelete }) => {
  const own = !isAdmin || app.owner_email === account.email;
  return (
    <tr>
      <td>
        <Link className="mono app-link" to={`/app/${app.name}`}>
          {app.name}
        </Link>
        <a className="open-link" href={app.url} target="_blank" rel="noreferrer">
          open -&gt;
        </a>
      </td>
      {isAdmin && <td className="cell-muted">{app.owner_email}</td>}
      <td className="mono">{app.port}</td>
      <td>
        {app.disk_mb} MB{own && ` of ${account.limits.disk_mb} MB`}
        {app.over_quota && <span className="badge badge-danger">over quota</span>}
      </td>
      <td className="cell-actions">
        <button type="button" className="btn btn-small btn-danger" onClick={() => onDelete(app)}>
          Delete
        </button>
      </td>
    </tr>
  );
};

const Dashboard = ({ account, refreshAccount }) => {
  const [apps, setApps] = useState(null);
  const [error, setError] = useState("");
  const [name, setName] = useState("");
  const [creating, setCreating] = useState(false);
  const navigate = useNavigate();

  const isAdmin = account.role === "admin";
  const atLimit = account.usage.apps >= account.limits.app_limit;
  const nameValid = nameRe.test(name);

  const load = useCallback(async () => {
    try {
      setApps(await api.get("/v1/apps"));
    } catch (err) {
      setError(err.message);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const create = async (e) => {
    e.preventDefault();
    if (!nameValid || creating) {
      return;
    }
    setCreating(true);
    setError("");
    try {
      const res = await api.post("/v1/apps", { name });
      setName("");
      refreshAccount();
      // Hand off to the app's page; the one-time private key (if hostit
      // generated one) rides along in router state and is never stored.
      navigate(`/app/${res.name}`, { state: res.private_key ? { private_key: res.private_key } : null });
    } catch (err) {
      setError(err.message);
    } finally {
      setCreating(false);
    }
  };

  const remove = async (app) => {
    if (!window.confirm(`Delete app "${app.name}"? This permanently deletes its container, files and user.`)) {
      return;
    }
    setError("");
    try {
      await api.del(`/v1/apps/${app.name}`);
      await load();
      refreshAccount();
    } catch (err) {
      setError(err.message);
    }
  };

  return (
    <>
      <div className="page-header">
        <h1>Apps</h1>
        <span className="usage">
          {account.usage.apps} of {account.limits.app_limit} apps used
        </span>
      </div>
      <ErrorBanner message={error} onDismiss={() => setError("")} />
      <div className="card">
        {apps === null && !error && <Loading label="Loading apps..." />}
        {apps !== null && apps.length === 0 && (
          <p className="empty">No apps yet. Create one below, or let Claude do it for you.</p>
        )}
        {apps !== null && apps.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  {isAdmin && <th>Owner</th>}
                  <th>Port</th>
                  <th>Disk</th>
                  <th aria-label="Actions" />
                </tr>
              </thead>
              <tbody>
                {apps.map((app) => (
                  <AppRow key={app.name} app={app} account={account} isAdmin={isAdmin} onDelete={remove} />
                ))}
              </tbody>
            </table>
          </div>
        )}
        {apps !== null && apps.length > 0 && (
          <p className="hint">Open an app to get a prompt you can paste into an AI assistant.</p>
        )}
        <form className="inline-form" onSubmit={create}>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="app name, e.g. blog"
            disabled={atLimit || creating}
            aria-label="New app name"
          />
          <button type="submit" className="btn btn-primary" disabled={atLimit || creating || !nameValid}>
            {creating ? "Creating..." : "New app"}
          </button>
        </form>
        {atLimit && <p className="hint">You have reached your app limit. Delete an app to create a new one.</p>}
        {!atLimit && name !== "" && !nameValid && (
          <p className="hint">Names are 2-32 characters: lowercase letters, digits and dashes, starting with a letter.</p>
        )}
      </div>
    </>
  );
};

export default Dashboard;
