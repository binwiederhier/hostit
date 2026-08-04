import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api } from "../api";
import { ErrorBanner, formatUsage, Loading, StatusDot } from "../components";

const nameRe = /^[a-z][a-z0-9-]{0,30}[a-z0-9]$/;

const AppRow = ({ app }) => (
  <tr>
    <td>
      <StatusDot running={app.running} />
      <Link className="mono app-link" to={`/app/${app.name}`}>
        {app.name}
      </Link>
      <a className="open-link" href={app.url} target="_blank" rel="noreferrer">
        open -&gt;
      </a>
    </td>
    <td>{formatUsage(app.memory_mb, app.memory_limit_mb)}</td>
    <td>
      {formatUsage(app.disk_mb, app.disk_limit_mb)}
      {app.over_quota && <span className="badge badge-danger">over quota</span>}
    </td>
    <td className="cell-actions">
      <Link className="btn btn-small btn-primary" to={`/app/${app.name}`}>
        Manage
      </Link>
    </td>
  </tr>
);

const Dashboard = ({ account, refreshAccount }) => {
  const [apps, setApps] = useState(null);
  const [error, setError] = useState("");
  const [name, setName] = useState("");
  const [creating, setCreating] = useState(false);
  const navigate = useNavigate();

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
                  <th>RAM</th>
                  <th>Disk</th>
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
