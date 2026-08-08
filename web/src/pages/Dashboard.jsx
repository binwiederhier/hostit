import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { api, isNetworkError } from "../api";
import { useReconnect } from "../hooks";
import { ErrorBanner, Loading, StatusDot, Wordmark } from "../components";

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
    </>
  );
};

// New app behind a modal, reached from the "New app" button. A dialog asks for
// the one thing needed -- the name -- instead of a field unfolding in place,
// which read as an odd half-state next to the app list.
const NewAppDialog = ({ name, setName, intent, setIntent, onSubmit, creating, atLimit, onCancel }) => {
  const valid = nameRe.test(name);
  const host = window.location.host;
  const sub = (name || "").replace(/[^a-z0-9-]/g, "") || "app";
  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onCancel}>
      <form className="card modal newapp" onSubmit={onSubmit} onMouseDown={(e) => e.stopPropagation()}>
        <div className="newapp-head">
          <div className="newapp-avatar">{sub.slice(0, 2)}</div>
          <div>
            <h2>Create an app</h2>
            <p className="newapp-sub">It gets its own container, subdomain and HTTPS certificate.</p>
          </div>
        </div>
        <label className="newapp-label">App name</label>
        <div className="newapp-input">
          <span className="newapp-dollar mono">$</span>
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. blog" aria-label="New app name" autoFocus disabled={creating} spellCheck="false" autoComplete="off" />
        </div>
        <div className="newapp-intent">
          <button type="button" className={"newapp-opt" + (intent === "host" ? " on" : "")} onClick={() => setIntent("host")}>
            <span className="t">{"\u{1F5C2}\u{FE0F}"} Host my files</span>
            <span className="d">Opens the file editor</span>
          </button>
          <button type="button" className={"newapp-opt" + (intent === "build" ? " on" : "")} onClick={() => setIntent("build")}>
            <span className="t">{"✨"} Build with AI</span>
            <span className="d">Opens the assistant</span>
          </button>
        </div>
        <div className="newapp-willbe">
          <div className="row">
            <span className="ico">{"\u{1F310}"}</span>
            <span className="lab">URL</span>
            <span className="val">https://<b>{sub}</b>.{host}</span>
          </div>
          <div className="row">
            <span className="ico">{"⌨️"}</span>
            <span className="lab">SSH</span>
            <span className="val">ssh <b>{sub}</b>@{host}</span>
          </div>
        </div>
        <p className="hint">{nameHint}</p>
        <div className="btn-row">
          <button type="button" className="btn" onClick={onCancel} disabled={creating}>
            Cancel
          </button>
          <button type="submit" className="btn btn-primary" disabled={creating || !valid || atLimit}>
            {creating ? "Creating..." : "Create app"}
          </button>
        </div>
      </form>
    </div>
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
    <td className="cell-name">
      <StatusDot running={app.running} appRunning={app.app_running} />
      <Link className="mono app-link" to={`/app/${app.name}`}>
        {app.name}
      </Link>
      {app.over_quota && <span className="badge badge-danger">over quota</span>}
    </td>
    {/* What the app says it is, from its hostit.yml */}
    <td className="cell-description">{app.description || <span className="cell-muted">no description yet</span>}</td>
    <td className="cell-actions">
      <div className="btn-row btn-row-end">
        <Link className="btn btn-small" to={`/app/${app.name}`}>
          Manage
        </Link>
        {/* Seeing the app is the common case, so it gets the accent and comes last */}
        <a className="btn btn-small btn-primary" href={app.url} target="_blank" rel="noreferrer">
          Open app
        </a>
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
  const [intent, setIntent] = useState("host"); // "host" -> editor, "build" -> assistant
  const inputRef = useRef(null);
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();

  // Opened here with ?new (e.g. from the nav's "+ New app"): show the dialog.
  useEffect(() => {
    if (searchParams.get("new") !== null) {
      setAdding(true);
      setSearchParams({}, { replace: true });
    }
  }, [searchParams, setSearchParams]);

  const atLimit = account.usage.apps >= account.limits.app_limit;
  const nameValid = nameRe.test(name);

  const load = useCallback(async () => {
    if (!navigator.onLine) return; // offline: don't hammer, wait for reconnect
    try {
      setApps(await api.get("/api/apps"));
      setError("");
    } catch (err) {
      // A background poll: a transient network blip keeps the last list rather
      // than flashing a sticky banner; only real errors surface.
      if (isNetworkError(err)) return;
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
  useReconnect(load); // refresh the list when connectivity or visibility returns

  const create = async (e) => {
    e.preventDefault();
    if (!nameValid || creating) {
      return;
    }
    setCreating(true);
    setError("");
    try {
      const res = await api.post("/api/apps", { name });
      // Remember the chosen intent as the app's first view (host -> editor,
      // build -> assistant); AppDetail reads and then keeps this per app.
      try {
        localStorage.setItem("hostit.view." + res.name, intent === "build" ? "assistant" : "editor");
      } catch {
        /* ignore storage failures */
      }
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

  const cancelAdding = () => {
    setAdding(false);
    setName("");
  };

  const formProps = { name, setName, onSubmit: create, creating, atLimit, inputRef };
  const empty = apps !== null && apps.length === 0;

  return (
    <>
      <div className="page-header">
        <h1>Apps</h1>
        <div className="header-actions">
          <span className="usage">
            {account.usage.apps} of {account.limits.app_limit} apps
          </span>
          {!empty && (
            <button type="button" className="btn btn-primary" onClick={() => setAdding(true)} disabled={atLimit}>
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
          {apps !== null && apps.length > 0 && (
            <>
              <div className="table-wrap">
                <table className="table-rows">
                  <thead>
                    <tr>
                      <th>Name</th>
                      <th>Description</th>
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
            </>
          )}
        </div>
      )}
      {adding && (
        <NewAppDialog name={name} setName={setName} intent={intent} setIntent={setIntent} onSubmit={create} creating={creating} atLimit={atLimit} onCancel={cancelAdding} />
      )}
    </>
  );
};

export default Dashboard;
