import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api } from "../api";
import { ErrorBanner, formatUsage, Loading, StatusDot, Wordmark } from "../components";

// Same rule the server enforces (app.AppNamePattern)
const nameRe = /^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$/;

const nameHint = "Names are up to 32 characters: lowercase letters, digits and dashes, starting with a letter.";

// The name form, shared by the empty state and the "New app" button. Both need
// a name before anything can happen, so the CTA is the submit button itself.
const CreateForm = ({ name, setName, onSubmit, creating, atLimit, big = false, inputRef }) => {
  const valid = nameRe.test(name);
  return (
    <>
      <form className={big ? "create-form create-form-big" : "create-form"} onSubmit={onSubmit}>
        <input
          ref={inputRef}
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="app name, e.g. blog"
          disabled={atLimit || creating}
          aria-label="New app name"
        />
        <button
          type="submit"
          className={big ? "btn btn-primary btn-big" : "btn btn-primary"}
          disabled={atLimit || creating || !valid}
        >
          {creating ? "Creating..." : "Create app"}
        </button>
      </form>
      {atLimit && <p className="hint">You have reached your app limit. Delete an app to create a new one.</p>}
      {!atLimit && name !== "" && !valid && <p className="hint">{nameHint}</p>}
    </>
  );
};

// What a brand-new account sees. It has to answer "what is this and what do I
// do now?" on its own, so: the mark, one line of pitch, and the one action.
const EmptyState = (props) => (
  <div className="card empty-state">
    <Wordmark big />
    <h2>Host your own mini-apps</h2>
    <p className="empty-pitch">
      Every app gets its own container, a subdomain with HTTPS, SSH access and an API token you can hand to an AI assistant.
      Name your first app and hostit will have it serving in seconds.
    </p>
    <CreateForm {...props} big />
    <p className="hint">{nameHint}</p>
  </div>
);

const AppRow = ({ app }) => (
  <tr>
    <td>
      <StatusDot running={app.running} />
      <Link className="mono app-link" to={`/app/${app.name}`}>
        {app.name}
      </Link>
    </td>
    <td>{formatUsage(app.memory_mb, app.memory_limit_mb)}</td>
    <td>
      {formatUsage(app.disk_mb, app.disk_limit_mb)}
      {app.over_quota && <span className="badge badge-danger">over quota</span>}
    </td>
    <td className="cell-actions">
      <div className="btn-row">
        {/* Seeing the app is the common case, so it gets the accent */}
        <a className="btn btn-small btn-primary" href={app.url} target="_blank" rel="noreferrer">
          Open
        </a>
        <Link className="btn btn-small" to={`/app/${app.name}`}>
          Manage
        </Link>
      </div>
    </td>
  </tr>
);

const Dashboard = ({ account, refreshAccount }) => {
  const [apps, setApps] = useState(null);
  const [error, setError] = useState("");
  const [name, setName] = useState("");
  const [creating, setCreating] = useState(false);
  const [adding, setAdding] = useState(false);
  const inputRef = useRef(null);
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

  // The server answers from a cache so the list renders at once; its numbers may
  // be a few seconds old, so ask again shortly after and then keep it fresh
  // while the page is open.
  useEffect(() => {
    load();
    const soon = setTimeout(load, 2000);
    const ticker = setInterval(load, 15000);
    return () => {
      clearTimeout(soon);
      clearInterval(ticker);
    };
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
      setAdding(false);
      refreshAccount();
      navigate(`/app/${res.name}`);
    } catch (err) {
      setError(err.message);
    } finally {
      setCreating(false);
    }
  };

  const startAdding = () => {
    setAdding(true);
    setTimeout(() => inputRef.current && inputRef.current.focus(), 0);
  };

  const formProps = { name, setName, onSubmit: create, creating, atLimit, inputRef };
  const empty = apps !== null && apps.length === 0;

  return (
    <>
      <div className="page-header">
        <h1>Apps</h1>
        <div className="header-actions">
          <span className="usage">
            {account.usage.apps} of {account.limits.app_limit} apps used
          </span>
          {!empty && !adding && (
            <button type="button" className="btn btn-primary" onClick={startAdding} disabled={atLimit}>
              New app
            </button>
          )}
        </div>
      </div>
      <ErrorBanner message={error} onDismiss={() => setError("")} />
      {empty && <EmptyState {...formProps} />}
      {!empty && (
        <div className="card">
          {apps === null && !error && <Loading label="Loading apps..." />}
          {adding && <CreateForm {...formProps} />}
          {apps !== null && apps.length > 0 && (
            <>
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
              <p className="hint">Open an app to get a prompt you can paste into an AI assistant.</p>
            </>
          )}
        </div>
      )}
    </>
  );
};

export default Dashboard;
